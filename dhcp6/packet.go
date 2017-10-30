package dhcp6

import (
	"fmt"
	"bytes"
)

type MessageType uint8

const (
	MsgSolicit MessageType = iota + 1
	MsgAdvertise
	MsgRequest
	MsgConfirm
	MsgRenew
	MsgRebind
	MsgReply
	MsgRelease
	MsgDecline
	MsgReconfigure
	MsgInformationRequest
	MsgRelayForw
	MsgRelayRepl
)

type Packet struct {
	Type          MessageType
	TransactionID [3]byte
	Options       Options
}

func MakePacket(bs []byte, packetLength int) (*Packet, error) {
	options, err := MakeOptions(bs[4:packetLength])
	if err != nil {
		return nil, fmt.Errorf("packet has malformed options section: %s", err)
	}
	ret := &Packet{Type: MessageType(bs[0]), Options: options}
	copy(ret.TransactionID[:], bs[1:4])
	return ret, nil
}

func (p *Packet) Marshal() ([]byte, error) {
	marshalled_options, err := p.Options.Marshal()
	if err != nil {
		return nil, fmt.Errorf("packet has malformed options section: %s", err)
	}

	ret := make([]byte, len(marshalled_options) + 4, len(marshalled_options) + 4)
	ret[0] = byte(p.Type)
	copy(ret[1:], p.TransactionID[:])
	copy(ret[4:], marshalled_options)

	return ret, nil
}

func (p *Packet) ShouldDiscard(serverDuid []byte) error {
	switch p.Type {
	case MsgSolicit:
		return ShouldDiscardSolicit(p)
	case MsgRequest:
		return ShouldDiscardRequest(p, serverDuid)
	case MsgInformationRequest:
		return ShouldDiscardInformationRequest(p, serverDuid)
	case MsgRelease:
		return nil // FIX ME!
	default:
		return fmt.Errorf("Unknown packet")
	}
}

func ShouldDiscardSolicit(p *Packet) error {
	options := p.Options
	if !options.HasBootFileUrlOption() {
		return fmt.Errorf("'Solicit' packet doesn't have file url option")
	}
	if !options.HasClientId() {
		return fmt.Errorf("'Solicit' packet has no client id option")
	}
	if options.HasServerId() {
		return fmt.Errorf("'Solicit' packet has server id option")
	}
	return nil
}

func ShouldDiscardRequest(p *Packet, serverDuid []byte) error {
	options := p.Options
	if !options.HasBootFileUrlOption() {
		return fmt.Errorf("'Request' packet doesn't have file url option")
	}
	if !options.HasClientId() {
		return fmt.Errorf("'Request' packet has no client id option")
	}
	if !options.HasServerId() {
		return fmt.Errorf("'Request' packet has no server id option")
	}
	if bytes.Compare(options.ServerId(), serverDuid) != 0 {
		return fmt.Errorf("'Request' packet's server id option (%d) is different from ours (%d)", options.ServerId(), serverDuid)
	}
	return nil
}

func ShouldDiscardInformationRequest(p *Packet, serverDuid []byte) error {
	options := p.Options
	if !options.HasBootFileUrlOption() {
		return fmt.Errorf("'Information-request' packet doesn't have boot file url option")
	}
	if options.HasIaNa() || options.HasIaTa() {
		return fmt.Errorf("'Information-request' packet has an IA option present")
	}
	if options.HasServerId() && (bytes.Compare(options.ServerId(), serverDuid) != 0) {
		return fmt.Errorf("'Information-request' packet's server id option (%d) is different from ours (%d)", options.ServerId(), serverDuid)
	}
	return nil
}