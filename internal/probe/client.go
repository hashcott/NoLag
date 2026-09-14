package probe

import (
	"context"
	"errors"
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
			if remaining <= 0 {
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
			if rtt < 0 {
				continue // clock moved backwards mid-run; discard rather than record a negative
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
send:
	for {
		select {
		case <-ctx.Done():
			break send
		case <-stopSending:
			break send
		case <-ticker.C:
			p := Packet{Seq: uint32(sent), SentUnixNano: time.Now().UnixNano()}
			if _, err := conn.Write(p.Marshal()); err != nil {
				// An ICMP port-unreachable from a previous probe surfaces here on
				// a connected UDP socket. That is a lost probe, not a failed run.
				sent++
				continue
			}
			sent++
		}
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	out := make([]time.Duration, len(rtts))
	copy(out, rtts)
	return stats.Summarize(sent, out), out, nil
}
