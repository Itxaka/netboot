// Copyright 2016 Google Inc.
// Copyright 2024 Kairos contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/kairos-io/netboot/constants"
	"github.com/kairos-io/netboot/dhcp4"
	"github.com/kairos-io/netboot/types"
)

func (s *Server) serveDHCP(conn *dhcp4.Conn) error {
	s.debug("DHCP", "Listening for DHCP requests on %s:%d", s.Address, s.DHCPPort)
	for {
		pkt, intf, err := conn.RecvDHCP()
		if err != nil {
			return fmt.Errorf("receiving DHCP packet: %s", err)
		}
		if intf == nil {
			return fmt.Errorf("received DHCP packet with no interface information (this is a violation of dhcp4.Conn's contract, please file a bug)")
		}

		if err = s.isBootDHCP(pkt); err != nil {
			s.debug("DHCP", "Ignoring packet from %s: %s", pkt.HardwareAddr, err)
			continue
		}
		mach, fwtype, err := s.validateDHCP(pkt)
		if err != nil {
			s.log("DHCP", "Unusable packet from %s: %s", pkt.HardwareAddr, err)
			continue
		}

		s.debug("DHCP", "Got valid request to boot %s (%s)", mach.MAC, mach.Arch)

		spec, err := s.Booter.BootSpec(mach)
		if err != nil {
			s.log("DHCP", "Couldn't get bootspec for %s: %s", pkt.HardwareAddr, err)
			continue
		}
		if spec == nil {
			s.debug("DHCP", "No boot spec for %s, ignoring boot request", pkt.HardwareAddr)
			s.machineEvent(pkt.HardwareAddr, machineStateIgnored, "Machine should not netboot")
			continue
		}

		s.log("DHCP", "Offering to boot %s", pkt.HardwareAddr)
		if fwtype == constants.FirmwarePixiecoreIpxe {
			s.machineEvent(pkt.HardwareAddr, machineStateProxyDHCPIpxe, "Offering to boot iPXE")
		} else {
			s.machineEvent(pkt.HardwareAddr, machineStateProxyDHCP, "Offering to boot")
		}

		// Machine should be booted.
		serverIP, err := interfaceIP(intf)
		if err != nil {
			s.log("DHCP", "Want to boot %s on %s, but couldn't get a source address: %s", pkt.HardwareAddr, intf.Name, err)
			continue
		}

		resp, err := s.offerDHCP(pkt, mach, serverIP, fwtype)
		if err != nil {
			s.log("DHCP", "Failed to construct ProxyDHCP offer for %s: %s", pkt.HardwareAddr, err)
			continue
		}

		if err = conn.SendDHCP(resp, intf); err != nil {
			s.log("DHCP", "Failed to send ProxyDHCP offer for %s: %s", pkt.HardwareAddr, err)
			continue
		}
	}
}

func (s *Server) isBootDHCP(pkt *dhcp4.Packet) error {
	if pkt.Type != dhcp4.MsgDiscover {
		return fmt.Errorf("packet is %s, not %s", pkt.Type, dhcp4.MsgDiscover)
	}

	if pkt.Options[dhcp4.OptClientSystem] == nil {
		return errors.New("not a PXE boot request (missing option 93)")
	}

	return nil
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func (s *Server) validateDHCP(pkt *dhcp4.Packet) (mach types.Machine, fwtype constants.Firmware, err error) {
	fwt, err := pkt.Options.Uint16(dhcp4.OptClientSystem)
	if err != nil {
		return mach, 0, fmt.Errorf("malformed DHCP option 93 (required for PXE): %s", err)
	}

	// RPI clients have a MAC address that starts with one of the following prefixes
	// Unfortunately they send a DHCP request with firmware type 0, so we need to
	// check the MAC address to identify them as we have to set some specific options
	rpiPrefixes := []string{"28:cd:c1:", "b8:27:eb:", "d8:3a:dd:", "dc:a6:32:", "e4:5f:01:"}
	switch {
	case hasAnyPrefix(pkt.HardwareAddr.String(), rpiPrefixes) && fwt == 0:
		s.debug("DHCP", "We think we got an RPI client")
		//mach.Arch = ArchArm64
		//fwtype = FirmwareRPIArm64
		//mach.MAC = pkt.HardwareAddr
		return mach, 0, fmt.Errorf("unsupported client firmware type '%d'", fwt)
	}

	// Basic architecture and firmware identification, based purely on
	// the PXE architecture option.
	switch fwt {
	case 0: // x86 BIOS
		s.debug("DHCP", "We got an x86 BIOS client")
		mach.Arch = constants.ArchIA32
		fwtype = constants.FirmwareX86PC
	case 6: // x86 UEFI
		s.debug("DHCP", "We got an x86 EFI client")
		mach.Arch = constants.ArchIA32
		fwtype = constants.FirmwareEFI32
	case 7: // x64 UEFI
		s.debug("DHCP", "We got an x64 EFI client")
		mach.Arch = constants.ArchX64
		fwtype = constants.FirmwareEFI64
	case 9: // EBC
		mach.Arch = constants.ArchX64
		fwtype = constants.FirmwareEFIBC
	case 11: // ARM 64-bit UEFI
		s.debug("DHCP", "We got an ARM64 client")
		mach.Arch = constants.ArchArm64
		fwtype = constants.FirmwareEfiArm64
	case 16: // http efi boot x64
		return mach, 0, fmt.Errorf("unsupported client firmware type (probably http4 x64 efi boot)")
	case 19: // http efi boot arm64
		return mach, 0, fmt.Errorf("unsupported client firmware type (probably http4 arm64 efi boot)")
	default:
		s.debug("DHCP", pkt.DebugString())
		return mach, 0, fmt.Errorf("unsupported client firmware type '%d'", fwt)
	}

	// Now, identify special sub-breeds of client firmware based on
	// the user-class option. Note these only change the "firmware
	// type", not the architecture we're reporting to Booters. We need
	// to identify these as part of making the internal chainloading
	// logic work properly.
	if userClass, err := pkt.Options.String(77); err == nil {
		// If the client has had iPXE burned into its ROM (or is a VM
		// that uses iPXE as the PXE "ROM"), special handling is
		// needed because in this mode the client is using iPXE native
		// drivers and chainloading to a UNDI stack won't work.
		if userClass == "iPXE" && fwtype == constants.FirmwareX86PC {
			fwtype = constants.FirmwareX86Ipxe
		}
		// If the client identifies as "pixiecore", we've already
		// chainloaded this client to the full-featured copy of iPXE
		// we supply. We have to distinguish this case so we don't
		// loop on the chainload step.
		if userClass == "pixiecore" {
			fwtype = constants.FirmwarePixiecoreIpxe
		}
	}

	guid := pkt.Options[dhcp4.OptUidGuidClientIdentifier]
	switch len(guid) {
	case 0:
		// A missing GUID is invalid according to the spec, however
		// there are PXE ROMs in the wild that omit the GUID and still
		// expect to boot. The only thing we do with the GUID is
		// mirror it back to the client if it's there, so we might as
		// well accept these buggy ROMs.
	case 17:
		if guid[0] != 0 {
			return mach, 0, errors.New("malformed client GUID (option 97), leading byte must be zero")
		}
	default:
		return mach, 0, errors.New("malformed client GUID (option 97), wrong size")
	}

	mach.MAC = pkt.HardwareAddr
	return mach, fwtype, nil
}

func (s *Server) offerDHCP(pkt *dhcp4.Packet, mach types.Machine, serverIP net.IP, fwtype constants.Firmware) (*dhcp4.Packet, error) {
	resp := &dhcp4.Packet{
		Type:          dhcp4.MsgOffer,
		TransactionID: pkt.TransactionID,
		Broadcast:     true,
		HardwareAddr:  mach.MAC,
		RelayAddr:     pkt.RelayAddr,
		ServerAddr:    serverIP,
		Options:       make(dhcp4.Options),
	}
	resp.Options[dhcp4.OptServerIdentifier] = serverIP
	// says the server should identify itself as a PXEClient vendor
	// type, even though it's a server. Strange.
	resp.Options[dhcp4.OptVendorIdentifier] = []byte("PXEClient")
	if pkt.Options[dhcp4.OptUidGuidClientIdentifier] != nil {
		resp.Options[dhcp4.OptUidGuidClientIdentifier] = pkt.Options[dhcp4.OptUidGuidClientIdentifier]
	}

	switch fwtype {
	case constants.FirmwareX86PC:
		// This is completely standard PXE: we tell the PXE client to
		// bypass all the boot discovery rubbish that PXE supports,
		// and just load a file from TFTP.

		pxe := dhcp4.Options{
			// PXE Boot Server Discovery Control - bypass, just boot from filename.
			6: []byte{8},
		}
		bs, err := pxe.Marshal()
		if err != nil {
			return nil, fmt.Errorf("failed to serialize PXE vendor options: %s", err)
		}
		resp.Options[dhcp4.OptVendorSpecific] = bs
		resp.BootServerName = serverIP.String()
		resp.BootFilename = fmt.Sprintf("%s/%d", mach.MAC, fwtype)

	case constants.FirmwareX86Ipxe:
		// Almost standard PXE, but the boot filename needs to be a URL.
		pxe := dhcp4.Options{
			// PXE Boot Server Discovery Control - bypass, just boot from filename.
			6: []byte{8},
		}
		bs, err := pxe.Marshal()
		if err != nil {
			return nil, fmt.Errorf("failed to serialize PXE vendor options: %s", err)
		}
		resp.Options[dhcp4.OptVendorSpecific] = bs
		resp.BootFilename = fmt.Sprintf("tftp://%s/%s/%d", serverIP, mach.MAC, fwtype)

	case constants.FirmwareEFI32, constants.FirmwareEFI64, constants.FirmwareEFIBC, constants.FirmwareEfiArm64:
		// In theory, the response we send for FirmwareX86PC should
		// also work for EFI. However, some UEFI firmwares don't
		// support PXE properly, and will ignore ProxyDHCP responses
		// that try to bypass boot server discovery control.
		//
		// On the other hand, seemingly all firmwares support a
		// variant of the protocol where option 43 is not
		// provided. They behave as if option 43 had pointed them to a
		// PXE boot server on port 4011 of the machine sending the
		// ProxyDHCP response. Looking at TianoCore sources, I believe
		// this is the BINL protocol, which is Microsoft-specific and
		// lacks a specification. However, empirically, this code
		// seems to work.
		//
		// So, for EFI, we just provide a server name and filename,
		// and expect to be called again on port 4011 (which is in
		// pxe.go).
		resp.BootServerName = serverIP.String()
		resp.BootFilename = fmt.Sprintf("%s/%d", mach.MAC, fwtype)

	case constants.FirmwarePixiecoreIpxe:
		// We've already gone through one round of chainloading, now
		// we can finally chainload to HTTP for the actual boot
		// script.
		resp.BootFilename = fmt.Sprintf("http://%s:%d/_/ipxe?arch=%d&mac=%s", serverIP, s.HTTPPort, mach.Arch, mach.MAC)
	default:
		return nil, fmt.Errorf("unknown firmware type %d", fwtype)
	}

	return resp, nil
}

func interfaceIP(intf *net.Interface) (net.IP, error) {
	addrs, err := intf.Addrs()
	if err != nil {
		return nil, err
	}

	// Try to find an IPv4 address to use, in the following order:
	// global unicast (includes rfc1918), link-local unicast,
	// loopback.
	fs := [](func(net.IP) bool){
		net.IP.IsGlobalUnicast,
		net.IP.IsLinkLocalUnicast,
		net.IP.IsLoopback,
	}
	for _, f := range fs {
		for _, a := range addrs {
			ipaddr, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipaddr.IP.To4()
			if ip == nil {
				continue
			}
			if f(ip) {
				return ip, nil
			}
		}
	}

	return nil, errors.New("no usable unicast address configured on interface")
}
