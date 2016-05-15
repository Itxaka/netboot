// Copyright 2016 Google Inc.
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

package dhcp

import (
	"errors"
	"io"
	"net"
	"time"

	"golang.org/x/net/ipv4"
)

// defined as a var so tests can override it.
var (
	dhcpClientPort = 68
	platformConn   func(string) (Conn, error)
)

// txType describes how a Packet should be sent on the wire.
type txType int

// The various transmission strategies described in RFC 2131. "MUST",
// "MUST NOT", "SHOULD" and "MAY" are as specified in RFC 2119.
const (
	// Packet MUST be broadcast.
	txBroadcast txType = iota
	// Packet MUST be unicasted to port 67 of RelayAddr
	txRelayAddr
	// Packet MUST be unicasted to port 68 of ClientAddr
	txClientAddr
	// Packet SHOULD be unicasted to port 68 of YourAddr, with the
	// link-layer destination explicitly set to HardwareAddr. You MUST
	// NOT rely on ARP resolution to discover the link-layer
	// destination address.
	//
	// Conn implementations that cannot explicitly set the link-layer
	// destination address MAY instead broadcast the packet.
	txHardwareAddr
)

// Conn is a DHCP-oriented packet socket.
//
// Multiple goroutines may invoke methods on a Conn simultaneously.
type Conn interface {
	io.Closer
	// RecvDHCP reads a Packet from the connection. It returns the
	// packet and the interface it was received on, which may be nil
	// if interface information cannot be obtained.
	RecvDHCP() (pkt *Packet, intf *net.Interface, err error)
	// SendDHCP sends pkt. The precise transmission mechanism depends
	// on pkt.txType(). intf should be the net.Interface returned by
	// RecvDHCP if responding to a DHCP client, or the interface for
	// which configuration is desired if acting as a client.
	SendDHCP(pkt *Packet, intf *net.Interface) error
	// SetReadDeadline sets the deadline for future Read calls.
	// If the deadline is reached, Read will fail with a timeout
	// (see type Error) instead of blocking.
	// A zero value for t means Read will not time out.
	SetReadDeadline(t time.Time) error
}

// NewConn creates a Conn bound to the given UDP ip:port.
func NewConn(addr string) (Conn, error) {
	if platformConn != nil {
		c, err := platformConn(addr)
		if err == nil {
			return c, nil
		}
	}
	// Always try falling back to the portable implementation
	return newPortableConn(addr)
}

type portableConn struct {
	conn *ipv4.PacketConn
}

func newPortableConn(addr string) (Conn, error) {
	c, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	l := ipv4.NewPacketConn(c)
	if err = l.SetControlMessage(ipv4.FlagInterface, true); err != nil {
		l.Close()
		return nil, err
	}
	return &portableConn{l}, nil
}

func (c *portableConn) Close() error {
	return c.conn.Close()
}

func (c *portableConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *portableConn) RecvDHCP() (*Packet, *net.Interface, error) {
	var buf [1500]byte
	for {
		n, cm, _, err := c.conn.ReadFrom(buf[:])
		if err != nil {
			return nil, nil, err
		}
		pkt, err := Unmarshal(buf[:n])
		if err != nil {
			continue
		}
		intf, err := net.InterfaceByIndex(cm.IfIndex)
		if err != nil {
			return nil, nil, err
		}
		// TODO: possibly more validation that the source lines up
		// with what the packet.
		return pkt, intf, nil
	}
}

func (c *portableConn) SendDHCP(pkt *Packet, intf *net.Interface) error {
	b, err := pkt.Marshal()
	if err != nil {
		return err
	}

	switch pkt.txType() {
	case txBroadcast, txHardwareAddr:
		cm := ipv4.ControlMessage{
			IfIndex: intf.Index,
		}
		addr := net.UDPAddr{
			IP:   net.IPv4bcast,
			Port: dhcpClientPort,
		}
		_, err = c.conn.WriteTo(b, &cm, &addr)
		return err
	case txRelayAddr:
		// Send to the server port, not the client port.
		addr := net.UDPAddr{
			IP:   pkt.RelayAddr,
			Port: 67,
		}
		_, err = c.conn.WriteTo(b, nil, &addr)
		return err
	case txClientAddr:
		addr := net.UDPAddr{
			IP:   pkt.ClientAddr,
			Port: dhcpClientPort,
		}
		_, err = c.conn.WriteTo(b, nil, &addr)
		return err
	default:
		return errors.New("unknown TX type for packet")
	}
}