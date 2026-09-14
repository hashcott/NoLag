package probe

import (
	"context"
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
