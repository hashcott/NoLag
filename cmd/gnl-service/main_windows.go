//go:build windows

// Command gnl-service is the privileged half of the GameNoLag client.
//
// It runs as LocalSystem because creating a Wintun adapter requires it —
// "run as administrator" is not enough and fails with access denied. Everything
// that touches the routing table, the adapter or the control plane lives here;
// the UI runs as an ordinary user and reaches this over a named pipe that
// carries four verbs and names nothing.
//
// Two safety properties hold whatever happens to this process. The adapter is
// created rather than reused, so it disappears when the process exits and takes
// every route pointing at it. The routes are written to the active store only,
// so a reboot clears them. A user can never be left with a machine that has no
// working network because this software stopped running.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"

	"gamenolag/internal/client/device"
	"gamenolag/internal/client/ipc"
	"gamenolag/internal/client/winpipe"
)

// serviceName is what Windows knows this service as.
const serviceName = "GameNoLag"

func main() {
	dir := dataDir()
	logf, closeLog := openLog(dir)
	defer closeLog()

	cfg, err := device.LoadConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		logf("%v", err)
		os.Exit(1)
	}
	key, err := device.LoadOrCreateKey(filepath.Join(dir, "device.key"))
	if err != nil {
		logf("%v", err)
		os.Exit(1)
	}
	e := newEngine(cfg, key, logf)

	inService, err := svc.IsWindowsService()
	if err != nil {
		logf("deciding whether this is a service: %v", err)
		os.Exit(1)
	}
	if !inService {
		// Started from a console. Useful for looking at a machine that is
		// misbehaving, and it still needs LocalSystem — psexec -s, or the service
		// itself. Ctrl-C tears everything down through the same path a stop does.
		logf("running in the console; stop with Ctrl-C")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		run(ctx, e, logf)
		return
	}
	if err := svc.Run(serviceName, &service{engine: e, logf: logf}); err != nil {
		logf("service stopped: %v", err)
		os.Exit(1)
	}
}

// service adapts the engine to the Windows service control manager.
type service struct {
	engine *engine
	logf   func(string, ...any)
}

func (s *service) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, s.engine, s.logf)
	}()
	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			// StopPending before the teardown, not after: removing routes and closing
			// the adapter takes long enough that Windows would otherwise decide the
			// service had hung and kill it part-way through.
			// WaitHint, or the control manager decides the service has hung and
			// kills it part-way through removing routes.
			status <- svc.Status{State: svc.StopPending, WaitHint: 20000}
			cancel()
			<-done
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		default:
			s.logf("ignoring an unexpected control request: %d", c.Cmd)
		}
	}
	cancel()
	<-done
	return false, 0
}

// run serves the pipe and watches for games until ctx is cancelled.
func run(ctx context.Context, e *engine, logf func(string, ...any)) {
	// Teardown on the way out whatever the reason. The routes are non-persistent
	// and the adapter dies with the process, so this is the orderly version of a
	// guarantee that holds anyway — but a player who stops the service should get
	// their ordinary network back at once, not at the next reboot.
	defer func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.teardown()
	}()

	pipeDone := make(chan struct{})
	go func() {
		defer close(pipeDone)
		err := winpipe.Serve(ctx, func(v ipc.Verb) ipc.Response {
			// Bounded: a UI that asks to connect while the control plane is hanging
			// must not hold the pipe, and with it every other request, indefinitely.
			reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			return e.handle(reqCtx, v)
		})
		if err != nil {
			logf("pipe: %v", err)
		}
	}()

	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			<-pipeDone
			return
		case <-t.C:
			e.poll()
		}
	}
}

// dataDir is where the configuration, the device key and the log live.
func dataDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "GameNoLag")
}

// maxLogBytes is when the log is rolled over. One previous file is kept, which
// is what somebody sending in a log after a bad evening actually needs.
const maxLogBytes = 8 << 20

func openLog(dir string) (func(string, ...any), func()) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return consoleLog(), func() {}
	}
	path := filepath.Join(dir, "service.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return consoleLog(), func() {}
	}
	// Both, so a console run shows what a service run would have written.
	l := log.New(io.MultiWriter(f, os.Stderr), "", log.LstdFlags|log.LUTC)
	return func(format string, args ...any) { l.Printf(format, args...) }, func() { f.Close() }
}

func consoleLog() func(string, ...any) {
	return func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
}
