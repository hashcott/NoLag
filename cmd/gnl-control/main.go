// Command gnl-control is the GameNoLag control plane.
//
//	gnl-control -dsn postgres://... -listen :8080
//	gnl-control -dsn postgres://... -mint-key
//
// It holds no private key for any relay or device: relays and clients generate
// their own and send only the public half.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gamenolag/internal/control"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("GNL_DSN"), "Postgres connection string (env GNL_DSN)")
	listen := flag.String("listen", ":8080", "HTTP listen address")
	staleAfter := flag.Duration("stale-after", 5*time.Minute,
		"mark a relay down after this long without a sync")
	poll := flag.Int("poll-secs", 10, "how often agents should sync")
	mintKey := flag.Bool("mint-key", false, "mint one contributor key, print it, and exit")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("-dsn is required (or set GNL_DSN)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := control.Open(ctx, *dsn)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	if *mintKey {
		key, err := store.CreateContributorKey(ctx)
		if err != nil {
			log.Fatalf("mint key: %v", err)
		}
		fmt.Println(key)
		fmt.Fprintln(os.Stderr, "This is shown once. Only its hash is stored.")
		return
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           control.NewServer(store, *poll),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	// A relay that stopped syncing is gone, and a gone relay must stop being
	// offered to players. Nothing else does this: status otherwise only ever moves
	// pending -> up, so a rebooted or switched-off relay would be handed out
	// forever.
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := store.MarkStaleRelaysDown(ctx, *staleAfter)
				if err != nil {
					log.Printf("mark stale relays: %v", err)
					continue
				}
				if n > 0 {
					log.Printf("marked %d relay(s) down after %s without a sync", n, *staleAfter)
				}
			}
		}
	}()

	log.Printf("control plane listening on %s", *listen)
	log.Printf("serve this behind TLS: relay tokens are bearer tokens and must never cross plain HTTP")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
