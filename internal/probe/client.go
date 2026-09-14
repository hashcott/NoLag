package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"gamenolag/internal/stats"
)

// ClientConfig describes one measurement run.
type ClientConfig struct {
	Target   string        // host:port of the echo server
	Rate     int           // probes per second; 0 means 20
	Duration time.Duration // how long to send for; 0 means 30s
	Grace    time.Duration // how long to keep reading after the last send; 0 means 1s
}

func (c ClientConfig) withDefaults() ClientConfig {
	if c.Rate <= 0 {
		c.Rate = 20
	}
	if c.Duration <= 0 {
		c.Duration = 30 * time.Second
	}
	if c.Grace <= 0 {
		c.Grace = time.Second
	}
	if c.Rate > 1000 {
		// time.Second/Rate truncates to zero at absurd rates and NewTicker panics.
		// A real game session is around 60 packets a second; 1000 is already far
		// past any useful probe rate.
		c.Rate = 1000
	}
	return c
}

// Run sends probes at a fixed rate and collects the echoes.
//
// A silent target is not an error: total loss is a measurement result, and the
// whole point of the tool is to record it. Only a target that cannot be resolved
// or a socket that cannot be opened returns an error.
func Run(ctx context.Context, cfg ClientConfig) (stats.Summary, []time.Duration, error) {
	cfg = cfg.withDefaults()

	addr, err := net.ResolveUDPAddr("udp", cfg.Target)
	if err != nil {
		return stats.Summary{}, nil, err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return stats.Summary{}, nil, err
	}
	defer conn.Close()

	// Unblock an in-flight Read when the context is cancelled. Without this the
	// reader sits in Read until the window elapses, so Ctrl-C appears to hang.
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stopCancel()

	var (
		mu   sync.Mutex
		rtts []time.Duration
		wg   sync.WaitGroup
	)

	readUntil := time.Now().Add(cfg.Duration + cfg.Grace)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 1500)
		for {
			remaining := time.Until(readUntil)
			if ctx.Err() != nil || remaining <= 0 {
				return
			}
			if err := conn.SetReadDeadline(time.Now().Add(remaining)); err != nil {
				return
			}
			n, err := conn.Read(buf)
			if err != nil {
				// A connected UDP socket delivers an ICMP port-unreachable provoked by
				// an earlier probe as an error here. That is one lost probe, not the end
				// of the run: returning would stop collecting and silently count every
				// later reply as loss, which is how a good provider gets disqualified by
				// a measurement bug. Only a closed socket ends this loop early; a timeout
				// falls through to the loop top, which returns when the window elapses.
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			p, err := Unmarshal(buf[:n])
			if err != nil {
				continue
			}
			// RTT comes from the timestamp we put in the packet ourselves, so no
			// per-packet bookkeeping is needed on this side.
			rtt := time.Duration(time.Now().UnixNano() - p.SentUnixNano)
			// Bound the sample by the read window. Nothing we sent can have been in
			// flight longer than that, so a sample outside the range is not our echo:
			// a stale or replayed datagram, or a backward clock step that landed
			// inside a flight. Admitting it would put arbitrary garbage into p99.
			if rtt < 0 || rtt > cfg.Duration+cfg.Grace {
				continue
			}
			mu.Lock()
			rtts = append(rtts, rtt)
			mu.Unlock()
		}
	}()

	interval := time.Second / time.Duration(cfg.Rate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	stopSending := time.After(cfg.Duration)

	sent := 0
	writeErrs := 0
send:
	for {
		select {
		case <-ctx.Done():
			break send
		case <-stopSending:
			break send
		case <-ticker.C:
			p := Packet{Seq: uint32(sent + writeErrs), SentUnixNano: time.Now().UnixNano()}
			if _, err := conn.Write(p.Marshal()); err != nil {
				// The datagram did not leave the host. On a connected UDP socket this
				// is usually a pending ICMP error from an earlier probe being consumed
				// here. Counting it as sent would inflate loss with probes that were
				// never on the wire, and loss is the number a provider is judged on.
				writeErrs++
				continue
			}
			sent++
		}
	}

	wg.Wait()

	if sent == 0 && writeErrs > 0 {
		// Nothing was ever transmitted. That is a fault on this machine, not a
		// measurement of the target: reporting it as 100% loss would be
		// indistinguishable from a target that is simply not answering, and those
		// are opposite diagnoses.
		return stats.Summary{}, nil, fmt.Errorf(
			"probe: could not send any datagram to %s: all %d write attempts failed",
			cfg.Target, writeErrs)
	}

	mu.Lock()
	defer mu.Unlock()
	out := make([]time.Duration, len(rtts))
	copy(out, rtts)
	return stats.Summarize(sent, out), out, nil
}
