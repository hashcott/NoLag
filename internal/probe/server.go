package probe

import (
	"context"
	"net"
	"time"
)

// Serve echoes probe packets back to their sender and drops everything else.
// It returns when ctx is cancelled.
//
// Access control is NOT done here. The echo server must be firewalled to the
// known measurement source addresses; see deploy/p0/install-probe.sh. A 1:1
// echo open to the internet is still a reflection vector.
func Serve(ctx context.Context, conn net.PacketConn) error {
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A read deadline is what makes cancellation observable: without it the
		// loop would block in ReadFrom until a packet happened to arrive.
		if err := conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
			return err
		}
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		if _, err := Unmarshal(buf[:n]); err != nil {
			continue // not ours, drop it rather than reflect it
		}
		// Echo the first PacketSize bytes verbatim: the client reads its own send
		// time back out of the echo to compute RTT.
		if _, err := conn.WriteTo(buf[:PacketSize], addr); err != nil {
			// Drop this echo and keep serving, exactly as the client does on its own
			// write errors. A transient ENOBUFS or a pending ICMP error costs one
			// sample; returning here would exit the process, and with Restart=always,
			// RestartSec=2 and systemd's default 5-starts-in-10s limit a short burst
			// would leave the unit failed for the rest of the campaign. The landmark
			// then goes dark and every leg A and C against it becomes a 100%-loss
			// record - which is the measurement failure the analyser must not be fed.
			continue
		}
	}
}
