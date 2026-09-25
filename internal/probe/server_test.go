package probe

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestServeEchoesValidPacket(t *testing.T) {
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, srv)

	cli, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	sent := Packet{Seq: 11, SentUnixNano: 1234567890}
	if _, err := cli.WriteTo(sent.Marshal(), srv.LocalAddr()); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := cli.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no echo received: %v", err)
	}
	got, err := Unmarshal(buf[:n])
	if err != nil {
		t.Fatalf("echo is not a probe packet: %v", err)
	}
	if got != sent {
		t.Errorf("echo = %+v, want %+v", got, sent)
	}
}

func TestServeDropsNonProbeTraffic(t *testing.T) {
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, srv)

	cli, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	if _, err := cli.WriteTo([]byte("hello, please reflect me"), srv.LocalAddr()); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64)
	cli.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := cli.ReadFrom(buf); err == nil {
		t.Error("server reflected a non-probe payload; it must drop it")
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s of cancel")
	}
}

// A valid probe packet followed by padding must draw a reply of exactly
// PacketSize bytes, never an echo of the whole datagram. Unmarshal tolerates
// trailing bytes by design, so without this test a change from
// conn.WriteTo(buf[:PacketSize], ...) to conn.WriteTo(buf[:n], ...) would turn
// the echo server into an amplification vector with every other test still green.
func TestServeDoesNotAmplifyPaddedPacket(t *testing.T) {
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, srv)

	cli, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	padded := append(Packet{Seq: 99, SentUnixNano: 1234}.Marshal(), make([]byte, 600)...)
	if _, err := cli.WriteTo(padded, srv.LocalAddr()); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 2048)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := cli.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no echo received: %v", err)
	}
	if n != PacketSize {
		t.Errorf("echo was %d bytes for a %d-byte request; want exactly %d. "+
			"An echo that grows with the request is an amplification vector.",
			n, len(padded), PacketSize)
	}
	got, err := Unmarshal(buf[:n])
	if err != nil {
		t.Fatalf("echo is not a probe packet: %v", err)
	}
	if got.Seq != 99 || got.SentUnixNano != 1234 {
		t.Errorf("echo = %+v, want Seq 99 SentUnixNano 1234", got)
	}
}

// failFirstWrite fails the first WriteTo and then behaves normally, standing in
// for a transient ENOBUFS or a pending ICMP error surfacing on the socket.
type failFirstWrite struct {
	net.PacketConn
	failed bool
}

func (f *failFirstWrite) WriteTo(b []byte, addr net.Addr) (int, error) {
	if !f.failed {
		f.failed = true
		return 0, errors.New("simulated transient write failure")
	}
	return f.PacketConn.WriteTo(b, addr)
}

// One failed write must cost one sample, not the campaign. Serve runs under
// Restart=always with systemd's default 5-starts-in-10s limit, so a process
// that exits on a transient write error leaves the unit permanently failed and
// the landmark dark for the rest of the week.
func TestServeSurvivesAWriteError(t *testing.T) {
	raw, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	srv := &failFirstWrite{PacketConn: raw}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv) }()

	cli, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	// First probe: the echo write fails and is dropped.
	first := Packet{Seq: 1, SentUnixNano: 1}
	if _, err := cli.WriteTo(first.Marshal(), raw.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	// Second probe: must still be answered, which it can only be if Serve is
	// still running.
	second := Packet{Seq: 2, SentUnixNano: 2}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := cli.WriteTo(second.Marshal(), raw.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		cli.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := cli.ReadFrom(buf)
		if err == nil {
			got, err := Unmarshal(buf[:n])
			if err != nil {
				t.Fatalf("echo is not a probe packet: %v", err)
			}
			if got != second {
				t.Errorf("echo = %+v, want %+v", got, second)
			}
			break
		}
		if time.Now().After(deadline) {
			select {
			case serveErr := <-done:
				t.Fatalf("Serve returned on a write error instead of continuing: %v", serveErr)
			default:
				t.Fatal("no echo after a failed write, but Serve is still running")
			}
		}
	}
}
