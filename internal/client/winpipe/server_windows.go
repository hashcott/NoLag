//go:build windows

// Package winpipe serves the named pipe the UI talks to.
//
// This is the client's only privilege boundary. The service holds LocalSystem
// because creating a Wintun adapter requires it; the UI holds nothing. So the
// pipe's access control is the single thing standing between any process on the
// machine and a service that can rewrite the routing table.
//
// Two defences, and both are needed. The descriptor below decides who may open
// the pipe at all. The four-verb protocol in internal/client/ipc decides what
// they may ask for once open — which is why even a caller who passes the ACL
// cannot name a file, an endpoint or a command.
package winpipe

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"

	"github.com/Microsoft/go-winio"

	"gamenolag/internal/client/ipc"
)

// PipeName is where the UI connects.
const PipeName = `\\.\pipe\GameNoLag`

// descriptor restricts the pipe in SDDL.
//
//	O:SY   owned by LocalSystem
//	G:SY   primary group LocalSystem
//	D:     a discretionary ACL that grants:
//	  (A;;GA;;;SY)  full access to LocalSystem — the service itself
//	  (A;;GA;;;BA)  full access to Administrators
//	  (A;;GRGW;;;IU) read and write to INTERACTIVE USERS
//
// INTERACTIVE rather than Everyone or Authenticated Users on purpose: it admits
// the person physically logged in and running the UI, and excludes service
// accounts, network logons and scheduled tasks. Without any ACL a named pipe is
// open to every process on the machine, and this one drives the routing table.
const descriptor = "O:SYG:SYD:(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

// Handler answers one request. It is given only a verb; see internal/client/ipc
// for why nothing else crosses this boundary.
type Handler func(ipc.Verb) ipc.Response

// Serve listens until ctx is cancelled.
func Serve(ctx context.Context, h Handler) error {
	l, err := winio.ListenPipe(PipeName, &winio.PipeConfig{
		SecurityDescriptor: descriptor,
		// One message per line of JSON, so a byte-mode pipe is what this wants.
		MessageMode:      false,
		InputBufferSize:  ipc.MaxLineBytes,
		OutputBufferSize: 8192,
	})
	if err != nil {
		return fmt.Errorf("winpipe: listen on %s: %w", PipeName, err)
	}
	defer l.Close()

	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("winpipe: accept: %w", err)
		}
		go handle(conn, h)
	}
}

func handle(conn net.Conn, h Handler) {
	defer conn.Close()

	// One request per connection. The UI opens the pipe, asks, reads the answer
	// and closes. Holding a connection open to send a stream of requests is not a
	// thing the UI does, and allowing it would mean one caller could hold the
	// boundary open indefinitely.
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 1024), ipc.MaxLineBytes)
	if !sc.Scan() {
		return
	}

	req, err := ipc.ParseRequest(sc.Bytes())
	if err != nil {
		// Logged, because something sending a verb this service does not know is
		// worth seeing in the log of a machine that had trouble - it is either a
		// version mismatch or something that is not the UI.
		log.Printf("winpipe: refused a request: %v", err)
		write(conn, ipc.Refuse(err))
		return
	}
	write(conn, h(req.Verb))
}

func write(conn net.Conn, resp ipc.Response) {
	b, err := ipc.Encode(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(b)
}
