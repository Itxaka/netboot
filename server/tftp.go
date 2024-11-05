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
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/kairos-io/netboot/constants"
	"github.com/kairos-io/netboot/tftp"
)

func (s *Server) serveTFTP(l net.PacketConn) error {
	s.debug("TFTP", "Listening for TFTP requests on %s:%d", s.Address, s.TFTPPort)
	ts := tftp.Server{
		Handler:     s.handleTFTP,
		InfoLog:     func(msg string) { s.debug("TFTP", msg) },
		TransferLog: s.logTFTPTransfer,
	}
	err := ts.Serve(l)
	if err != nil {
		return fmt.Errorf("TFTP server shut down: %s", err)
	}
	return nil
}

func extractInfo(path string) (net.HardwareAddr, int, error) {
	pathElements := strings.Split(path, "/")
	if len(pathElements) != 2 {
		return nil, 0, errors.New("not found")
	}

	mac, err := net.ParseMAC(pathElements[0])
	if err != nil {
		return nil, 0, fmt.Errorf("invalid MAC address %q", pathElements[0])
	}

	i, err := strconv.Atoi(pathElements[1])
	if err != nil {
		return nil, 0, errors.New("not found")
	}

	return mac, i, nil
}

func (s *Server) logTFTPTransfer(clientAddr net.Addr, path string, err error) {
	if strings.HasPrefix(path, "pxelinux.cfg/") {
		if err != nil {
			// debug logging because pxelinux is very noisy as it does a lot of requests
			s.debug("TFTP", "Send of %q to %s failed: %s", path, clientAddr, err)
		} else {
			_, i, _ := extractInfoPxeLinux(path)
			s.log("TFTP", "Sent %q to %s", i, clientAddr)
			//s.machineEvent(mac, machineStateTFTP, "Sent Pxelinux asset to %s", clientAddr)
		}
	} else {
		mac, _, pathErr := extractInfo(path)
		if pathErr != nil {
			s.log("TFTP", "unable to extract mac from request:%v", pathErr)
			return
		}
		if err != nil {
			s.log("TFTP", "Send of %q to %s failed: %s", path, clientAddr, err)
		} else {
			s.log("TFTP", "Sent %q to %s", mac.String(), clientAddr)
			s.machineEvent(mac, machineStateTFTP, "Sent iPXE to %s", clientAddr)
		}
	}
}

func (s *Server) handleTFTP(path string, clientAddr net.Addr) (io.ReadCloser, int64, error) {
	if strings.HasPrefix(path, "pxelinux.cfg/") {
		_, i, err := extractInfoPxeLinux(path)
		if err != nil {
			return nil, 0, fmt.Errorf("unknown path %q", path)
		}
		f, ok := s.PxeLinuxAssets[i]
		if !ok {
			return nil, 0, fmt.Errorf("PxeLinux asset not found with key %s", i)
		}
		bs, err := os.ReadFile(f)
		if err != nil {
			s.log("TFTP", "Failed to read file %q: %s", f, err)
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewBuffer(bs)), int64(len(bs)), nil
	} else {
		_, i, err := extractInfo(path)
		if err != nil {
			return nil, 0, fmt.Errorf("unknown path %q", path)
		}

		bs, ok := s.Ipxe[constants.Firmware(i)]
		if !ok {
			return nil, 0, fmt.Errorf("unknown firmware type %d", i)
		}

		return io.NopCloser(bytes.NewBuffer(bs)), int64(len(bs)), nil
	}
}

func extractInfoPxeLinux(path string) (net.HardwareAddr, string, error) {
	var mac net.HardwareAddr
	pathElements := strings.Split(path, "/")
	if len(pathElements) != 2 {
		return nil, "", errors.New("not found")
	}
	cleanedMac := strings.Replace(pathElements[1], "01-", "", 1)
	mac, _ = net.ParseMAC(cleanedMac)
	// We return:
	// parsed Mac address if possible
	// the filename of the pxelinux config requested
	return mac, pathElements[1], nil
}
