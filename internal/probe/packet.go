// Package probe measures one-way-capable UDP round trip time against a host we
// control, so that the target answers every packet instead of rate-limiting or
// ignoring it the way a real game server or a random internet host does.
package probe

import (
	"encoding/binary"
	"errors"
)

// PacketSize is the exact size of a probe packet on the wire.
const PacketSize = 16

// magic marks a packet as ours. Without it the echo server would reflect any
// UDP payload sent to it, which turns a measurement tool into a reflector.
var magic = [4]byte{'G', 'N', 'L', 'P'}

// ErrBadPacket is returned for anything that is not a probe packet.
var ErrBadPacket = errors.New("probe: not a probe packet")

// Packet is one probe. The send time travels inside it and comes back in the
// echo, so the client computes RTT without keeping per-packet state.
type Packet struct {
	Seq          uint32
	SentUnixNano int64
}

// Marshal encodes p into exactly PacketSize bytes.
func (p Packet) Marshal() []byte {
	b := make([]byte, PacketSize)
	copy(b[0:4], magic[:])
	binary.BigEndian.PutUint32(b[4:8], p.Seq)
	binary.BigEndian.PutUint64(b[8:16], uint64(p.SentUnixNano))
	return b
}

// Unmarshal decodes a probe packet. Trailing bytes are ignored: a datagram that
// starts with a valid probe is a valid probe.
func Unmarshal(b []byte) (Packet, error) {
	if len(b) < PacketSize {
		return Packet{}, ErrBadPacket
	}
	if b[0] != magic[0] || b[1] != magic[1] || b[2] != magic[2] || b[3] != magic[3] {
		return Packet{}, ErrBadPacket
	}
	return Packet{
		Seq:          binary.BigEndian.Uint32(b[4:8]),
		SentUnixNano: int64(binary.BigEndian.Uint64(b[8:16])),
	}, nil
}
