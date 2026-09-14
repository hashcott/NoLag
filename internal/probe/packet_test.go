package probe

import (
	"errors"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	in := Packet{Seq: 4242, SentUnixNano: 1757836800123456789}
	b := in.Marshal()
	if len(b) != PacketSize {
		t.Fatalf("Marshal produced %d bytes, want %d", len(b), PacketSize)
	}
	out, err := Unmarshal(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}

func TestUnmarshalRejectsShort(t *testing.T) {
	_, err := Unmarshal(make([]byte, PacketSize-1))
	if !errors.Is(err, ErrBadPacket) {
		t.Errorf("err = %v, want ErrBadPacket", err)
	}
}

func TestUnmarshalRejectsWrongMagic(t *testing.T) {
	b := Packet{Seq: 1}.Marshal()
	b[0] = 'X'
	_, err := Unmarshal(b)
	if !errors.Is(err, ErrBadPacket) {
		t.Errorf("err = %v, want ErrBadPacket", err)
	}
}

func TestUnmarshalIgnoresTrailingBytes(t *testing.T) {
	b := append(Packet{Seq: 7, SentUnixNano: 99}.Marshal(), 0xAA, 0xBB)
	out, err := Unmarshal(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Seq != 7 || out.SentUnixNano != 99 {
		t.Errorf("out = %+v, want Seq 7 SentUnixNano 99", out)
	}
}
