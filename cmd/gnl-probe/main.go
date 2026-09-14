// Command gnl-probe measures UDP round trip time between two hosts we control.
//
//	gnl-probe server -listen :51830
//	gnl-probe client -target 203.0.113.10:51830 -leg B -from vn-viettel -to vps-sgp -out results.jsonl
//
// The tool exists because the thing being optimised is UDP game traffic, and
// ICMP is rate-limited or deprioritised on enough of the path that it would
// measure something other than what players experience.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gamenolag/internal/probe"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "server":
		os.Exit(runServer(os.Args[2:]))
	case "client":
		os.Exit(runClient(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `gnl-probe measures UDP round trip time against a host we control.

  gnl-probe server -listen :51830
  gnl-probe client -target HOST:PORT -leg A|B|C -from NAME -to NAME -out FILE

Run "gnl-probe server -h" or "gnl-probe client -h" for the full flag list.
`)
}

func runServer(args []string) int {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	listen := fs.String("listen", ":51830", "UDP address to listen on")
	fs.Parse(args)

	conn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen %s: %v\n", *listen, err)
		return 1
	}
	defer conn.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("echo server listening on %s\n", conn.LocalAddr())
	fmt.Println("reminder: firewall this port to the measurement source addresses only")
	if err := probe.Serve(ctx, conn); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}

func runClient(args []string) int {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	target := fs.String("target", "", "echo server address, host:port (required)")
	leg := fs.String("leg", "", "which leg this measures: A, B or C (required)")
	from := fs.String("from", "", "name of the machine measuring (required)")
	to := fs.String("to", "", "name of the machine being measured (required)")
	out := fs.String("out", "results.jsonl", "results file to append to")
	rate := fs.Int("rate", 20, "probes per second")
	duration := fs.Duration("duration", 60*time.Second, "how long to send for")
	fs.Parse(args)

	missing := ""
	switch {
	case *target == "":
		missing = "-target"
	case *leg == "":
		missing = "-leg"
	case *from == "":
		missing = "-from"
	case *to == "":
		missing = "-to"
	}
	if missing != "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", missing)
		fs.Usage()
		return 2
	}
	if *leg != "A" && *leg != "B" && *leg != "C" {
		fmt.Fprintf(os.Stderr, "-leg must be A, B or C, got %q\n", *leg)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sum, _, err := probe.Run(ctx, probe.ClientConfig{
		Target:   *target,
		Rate:     *rate,
		Duration: *duration,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe %s: %v\n", *target, err)
		return 1
	}

	rec := probe.Record{
		TS:      time.Now().UTC(),
		Leg:     *leg,
		From:    *from,
		To:      *to,
		Target:  *target,
		Summary: sum,
	}
	if err := probe.Append(*out, rec); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
		return 1
	}

	fmt.Printf("leg %s  %s -> %s  sent %d  loss %.2f%%  p50 %.1fms  p95 %.1fms  p99 %.1fms  jitter %.1fms\n",
		rec.Leg, rec.From, rec.To, sum.Sent, sum.LossPct, sum.P50Ms, sum.P95Ms, sum.P99Ms, sum.JitterMs)
	return 0
}
