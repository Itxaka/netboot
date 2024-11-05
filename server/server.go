// Copyright 2016 Google Inc.
// Copyright 2024 Kairos contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/kairos-io/netboot/assets"
	"github.com/kairos-io/netboot/constants"
	"github.com/kairos-io/netboot/dhcp4"
	"github.com/kairos-io/netboot/dhcp6"
	"github.com/kairos-io/netboot/types"
)

// A Server boots machines using a Booter.
type Server struct {
	Booter types.Booter

	// Address to listen on, or empty for all interfaces.
	Address string
	// HTTP port for boot services.
	HTTPPort int

	// Ipxe lists the supported bootable Firmwares, and their
	// associated ipxe binary.
	Ipxe map[constants.Firmware][]byte
	// PxeLinuxAssets lists the supported map of PxeLinux assets.
	// It links the mac/default to the pxe config file.
	// Usually the PxeLinux references are referred by several names, like:
	// The client UUID, if provided by the PXE stack.
	//
	// Note that some BIOSes do not have a valid UUID, and it might end up reporting something like all 1's.
	// This value is represented in the standard UUID format using lowercase hexadecimal digits, e.g. "b8945908-d6a6-41a9-611d-74a6ab80b83d".
	//
	// The hardware type (using its ARP "htype" code) and address, all in lowercase hexadecimal with dash separators
	// For example, for an Ethernet (i.e. ARP hardware type "1") with address "88:99:AA:BB:CC:DD",
	//  it would search for the filename "01-88-99-aa-bb-cc-dd".
	//
	// The client's own IPv4 address in uppercase hexadecimal, followed by removing hex characters, one at a time, from the end.
	// For example, "192.168.2.91" → "C0A8025B". Then "C0A8025", "C0A802", "C0A80", "C0A8", "C0A", "C0", "C"
	//
	// Lowercase "default" string
	// This is useful to provide configs for just one client or a group of clients. And have a generic fallback for all other clients.
	PxeLinuxAssets map[string]string

	// Log receives logs on Pixiecore's operation. If nil, logging
	// is suppressed.
	Log func(subsystem, msg string)
	// Debug receives extensive logging on Pixiecore's internals. Very
	// useful for debugging, but very verbose.
	Debug func(subsystem, msg string)
	// These ports can technically be set for testing, but the
	// protocols burned in firmware on the client side hardcode these,
	// so if you change them in production, nothing will work.
	DHCPPort int
	TFTPPort int
	PXEPort  int

	// Listen for DHCP traffic without binding to the DHCP port. This
	// enables coexistence of Pixiecore with another DHCP server.
	//
	// Currently only supported on Linux.
	DHCPNoBind bool

	// Read UI assets from this path, rather than use the builtin UI
	// assets. Used for development of Pixiecore.
	UIAssetsDir string

	errs chan error

	eventsMu sync.Mutex
	events   map[string][]machineEvent
}

// SetDefaultFirmwares sets the default bundled ipxe binaries for the server
func (s *Server) SetDefaultFirmwares() {
	s.Ipxe = map[constants.Firmware][]byte{}
	s.Ipxe[constants.FirmwareX86PC] = assets.MustAsset("undionly.kpxe")
	s.Ipxe[constants.FirmwareEFI32] = assets.MustAsset("i386.ipxe.efi")
	s.Ipxe[constants.FirmwareEFI64] = assets.MustAsset("amd64.ipxe.efi")
	s.Ipxe[constants.FirmwareEFIBC] = assets.MustAsset("amd64.ipxe.efi")
	s.Ipxe[constants.FirmwareEfiArm64] = assets.MustAsset("arm64.ipxe.efi")
	s.Ipxe[constants.FirmwareX86Ipxe] = assets.MustAsset("ipxe.pxe")
}

// Serve listens for machines attempting to boot, and uses Booter to
// help them.
func (s *Server) Serve() error {
	if s.DHCPPort == 0 {
		s.DHCPPort = constants.PortDHCP
	}
	if s.TFTPPort == 0 {
		s.TFTPPort = constants.PortTFTP
	}
	if s.PXEPort == 0 {
		s.PXEPort = constants.PortPXE
	}
	if s.HTTPPort == 0 {
		s.HTTPPort = constants.PortHTTP
	}

	newDHCP := dhcp4.NewConn
	if s.DHCPNoBind {
		newDHCP = dhcp4.NewSnooperConn
	}
	if s.Address == "" {
		s.Address = "0.0.0.0"
	}

	dhcp, err := newDHCP(fmt.Sprintf("%s:%d", s.Address, s.DHCPPort))
	if err != nil {
		return err
	}
	tftp, err := net.ListenPacket("udp", fmt.Sprintf("%s:%d", s.Address, s.TFTPPort))
	if err != nil {
		dhcp.Close()
		return err
	}
	pxe, err := net.ListenPacket("udp4", fmt.Sprintf("%s:%d", s.Address, s.PXEPort))
	if err != nil {
		dhcp.Close()
		tftp.Close()
		return err
	}
	http, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.Address, s.HTTPPort))
	if err != nil {
		dhcp.Close()
		tftp.Close()
		pxe.Close()
		return err
	}

	s.events = make(map[string][]machineEvent)
	// 5 buffer slots, one for each goroutine, plus one for
	// Shutdown(). We only ever pull the first error out, but shutdown
	// will likely generate some spurious errors from the other
	// goroutines, and we want them to be able to dump them without
	// blocking.
	s.errs = make(chan error, 6)

	s.debug("Init", "Starting Pixiecore goroutines")

	go func() { s.errs <- s.serveDHCP(dhcp) }()
	go func() { s.errs <- s.servePXE(pxe) }()
	go func() { s.errs <- s.serveTFTP(tftp) }()
	go func() { s.errs <- serveHTTP(http, s.serveHTTP) }()

	// Wait for either a fatal error, or Shutdown().
	err = <-s.errs
	dhcp.Close()
	tftp.Close()
	pxe.Close()
	http.Close()
	return err
}

// Shutdown causes Serve() to exit, cleaning up behind itself.
func (s *Server) Shutdown() {
	select {
	case s.errs <- nil:
	default:
	}
}

// ServerV6 boots machines using a Booter.
type ServerV6 struct {
	Address string
	Port    int
	Duid    []byte

	BootConfig    dhcp6.BootConfiguration
	PacketBuilder *dhcp6.PacketBuilder
	AddressPool   types.AddressPool

	errs chan error

	Log   func(subsystem, msg string)
	Debug func(subsystem, msg string)
}

// NewServerV6 returns a new ServerV6.
func NewServerV6() *ServerV6 {
	ret := &ServerV6{
		Port: constants.PortDHCPv6,
	}
	return ret
}

// Serve listens for machines attempting to boot, and responds to
// their DHCPv6 requests.
func (s *ServerV6) Serve() error {
	s.log("dhcp", "starting...")

	dhcp, err := dhcp6.NewConn(s.Address, s.Port)
	if err != nil {
		return err
	}

	s.debug("dhcp", "new connection...")

	// 5 buffer slots, one for each goroutine, plus one for
	// Shutdown(). We only ever pull the first error out, but shutdown
	// will likely generate some spurious errors from the other
	// goroutines, and we want them to be able to dump them without
	// blocking.
	s.errs = make(chan error, 6)

	s.setDUID(dhcp.SourceHardwareAddress())

	go func() { s.errs <- s.serveDHCP(dhcp) }()

	// Wait for either a fatal error, or Shutdown().
	err = <-s.errs
	dhcp.Close()

	s.log("dhcp", "stopped...")
	return err
}

// Shutdown causes Serve() to exit, cleaning up behind itself.
func (s *ServerV6) Shutdown() {
	select {
	case s.errs <- nil:
	default:
	}
}

func (s *ServerV6) log(subsystem, format string, args ...interface{}) {
	if s.Log == nil {
		return
	}
	s.Log(subsystem, fmt.Sprintf(format, args...))
}

func (s *ServerV6) debug(subsystem, format string, args ...interface{}) {
	if s.Debug == nil {
		return
	}
	s.Debug(subsystem, fmt.Sprintf(format, args...))
}

func (s *ServerV6) setDUID(addr net.HardwareAddr) {
	duid := make([]byte, len(addr)+8) // see rfc3315, section 9.2, DUID-LT

	copy(duid[0:], []byte{0, 1}) //fixed, x0001
	copy(duid[2:], []byte{0, 1}) //hw type ethernet, x0001

	utcLoc, _ := time.LoadLocation("UTC")
	sinceJanFirst2000 := time.Since(time.Date(2000, time.January, 1, 0, 0, 0, 0, utcLoc))
	binary.BigEndian.PutUint32(duid[4:], uint32(sinceJanFirst2000.Seconds()))

	copy(duid[8:], addr)

	s.Duid = duid
}
