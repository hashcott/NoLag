package probe

import (
	"context"
	"net"
	"testing"
	"time"
)

// startEchoServer runs Serve on an ephemeral loopback port and returns its address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go Serve(ctx, srv)
	t.Cleanup(func() { cancel(); srv.Close() })
	return srv.LocalAddr().String()
}

func TestRunAgainstLiveEchoServer(t *testing.T) {
	addr := startEchoServer(t)

	sum, rtts, err := Run(context.Background(), ClientConfig{
		Target:   addr,
		Rate:     50,
		Duration: 400 * time.Millisecond,
		Grace:    500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Sent < 15 {
		t.Errorf("Sent = %d, want at least 15 at 50 Hz for 400ms", sum.Sent)
	}
	if sum.Received != sum.Sent {
		t.Errorf("Received = %d, Sent = %d; loopback should lose nothing", sum.Received, sum.Sent)
	}
	if sum.LossPct != 0 {
		t.Errorf("LossPct = %v, want 0 on loopback", sum.LossPct)
	}
	if len(rtts) != sum.Received {
		t.Errorf("len(rtts) = %d, want %d", len(rtts), sum.Received)
	}
	if sum.P50Ms < 0 || sum.P50Ms > 100 {
		t.Errorf("P50Ms = %v, implausible on loopback", sum.P50Ms)
	}
}

func TestRunCountsLossWhenNothingAnswers(t *testing.T) {
	// Port 1 on loopback: nothing listens, so every probe is lost.
	sum, rtts, err := Run(context.Background(), ClientConfig{
		Target:   "127.0.0.1:1",
		Rate:     20,
		Duration: 200 * time.Millisecond,
		Grace:    200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run must not fail when the target is silent: %v", err)
	}
	if sum.Received != 0 {
		t.Errorf("Received = %d, want 0", sum.Received)
	}
	if sum.LossPct != 100 {
		t.Errorf("LossPct = %v, want 100", sum.LossPct)
	}
	if len(rtts) != 0 {
		t.Errorf("len(rtts) = %d, want 0", len(rtts))
	}
}

func TestRunRejectsUnresolvableTarget(t *testing.T) {
	_, _, err := Run(context.Background(), ClientConfig{
		Target:   "no-such-host.invalid:51830",
		Duration: 100 * time.Millisecond,
	})
	if err == nil {
		t.Error("want an error for an unresolvable target")
	}
}

func TestRunStopsPromptlyWhenContextCancelled(t *testing.T) {
	addr := startEchoServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _, err := Run(ctx, ClientConfig{
		Target:   addr,
		Rate:     20,
		Duration: 30 * time.Second,
		Grace:    time.Second,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("cancellation is not a failure: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Run took %v after a cancel at 200ms; it must not wait out the "+
			"full 31s window", elapsed)
	}
}

func TestRunAppliesDefaults(t *testing.T) {
	addr := startEchoServer(t)
	// Duration is set small; Rate and Grace are left zero and must default.
	sum, _, err := Run(context.Background(), ClientConfig{
		Target:   addr,
		Duration: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Default rate is 20 Hz, so 300ms yields roughly 6 probes.
	if sum.Sent < 3 || sum.Sent > 12 {
		t.Errorf("Sent = %d, want roughly 6 at the default 20 Hz", sum.Sent)
	}
}
