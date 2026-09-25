// Package agent is the relay-side reconcile loop.
//
// It pulls the complete desired state from the control plane and applies the
// difference. Pull rather than push means the relay needs no inbound port
// besides WireGuard, and the control plane needs no retry queue: a dropped
// response is simply retried on the next tick.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gamenolag/internal/api"
	"gamenolag/internal/ipsetsync"
	"gamenolag/internal/wgsync"
)

// Version is stamped at build time with -ldflags.
var Version = "dev"

// Config is what the agent needs to run.
type Config struct {
	ControlURL string
	Token      string
	Iface      string
	SetName    string
	Poll       time.Duration
}

// Syncer is the control plane as the loop sees it.
type Syncer interface {
	Sync(ctx context.Context, st api.RelayStatus) (api.SyncResponse, error)
}

// RunOnce performs one reconcile pass: report status, fetch desired state,
// apply peers, apply the allowlist.
//
// Peers and the allowlist are applied independently and their errors are
// joined. A failure in one must not block the other: a stale allowlist is worse
// than a fresh one, but nobody being able to connect is worse than both.
func RunOnce(
	ctx context.Context,
	dev wgsync.Device,
	cp Syncer,
	sets *ipsetsync.Syncer,
	iface string,
	applyIPSet func(setName string, sorted []string) error,
	reassertFirewall func() error,
) error {
	st, err := dev.Stats(iface)
	if err != nil {
		// Report what we can rather than skipping the sync: the control plane
		// learning nothing is worse than it learning zeroes.
		log.Printf("agent: read stats from %s: %v", iface, err)
	}

	status := api.RelayStatus{
		ActivePeers:  st.ActivePeers,
		TotalPeers:   st.TotalPeers,
		RxBytes:      st.RxBytes,
		TxBytes:      st.TxBytes,
		LoadAvg1:     loadAvg1(),
		AgentVersion: Version,
	}

	resp, err := cp.Sync(ctx, status)
	if err != nil {
		// Return without touching anything. The peers already on the device stay
		// exactly as they are, so a control plane outage never disconnects a
		// player who is mid-match.
		return fmt.Errorf("agent: sync with control plane: %w", err)
	}

	var errs []error

	current, err := dev.Peers(iface)
	if err != nil {
		errs = append(errs, fmt.Errorf("agent: read peers: %w", err))
	} else if change := wgsync.Diff(current, resp.Peers); !change.Empty() {
		if err := dev.Apply(iface, change); err != nil {
			errs = append(errs, fmt.Errorf("agent: apply peers: %w", err))
		} else {
			log.Printf("agent: peers +%d ~%d -%d (now %d)",
				len(change.Add), len(change.Update), len(change.Remove), len(resp.Peers))
		}
	}

	if err := sets.Sync(resp.GameCIDRs, applyIPSet); err != nil {
		errs = append(errs, fmt.Errorf("agent: apply allowlist: %w", err))
	}

	// Re-assert the egress policy every poll. The installer sets it once and
	// exits, so nothing owns it afterwards: anything that puts FORWARD back to
	// ACCEPT turns this relay into an open forwarder on the contributor's own IP,
	// silently, because the five rules are all ACCEPT and block nothing by
	// themselves. Idempotent, so the steady state is one -C per rule.
	if reassertFirewall != nil {
		if err := reassertFirewall(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// Loop runs RunOnce on a timer until ctx is cancelled.
func Loop(
	ctx context.Context,
	cfg Config,
	dev wgsync.Device,
	cp Syncer,
	applyIPSet func(setName string, sorted []string) error,
	reassertFirewall func() error,
) {
	sets := ipsetsync.New(cfg.SetName)
	ticker := time.NewTicker(cfg.Poll)
	defer ticker.Stop()

	for {
		if err := RunOnce(ctx, dev, cp, sets, cfg.Iface, applyIPSet, reassertFirewall); err != nil {
			log.Printf("agent: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// HTTPSyncer talks to the control plane over HTTP.
type HTTPSyncer struct {
	URL    string
	Token  string
	Client *http.Client
}

// NewHTTPSyncer returns a Syncer pointed at the control plane.
func NewHTTPSyncer(baseURL, token string) *HTTPSyncer {
	return &HTTPSyncer{
		URL:    strings.TrimSuffix(baseURL, "/") + "/v1/relay/sync",
		Token:  token,
		Client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Sync posts status and returns the desired state.
func (h *HTTPSyncer) Sync(ctx context.Context, st api.RelayStatus) (api.SyncResponse, error) {
	body, err := json.Marshal(api.SyncRequest{Status: st})
	if err != nil {
		return api.SyncResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return api.SyncResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.Token)

	resp, err := h.Client.Do(req)
	if err != nil {
		return api.SyncResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e api.ErrorResponse
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		if e.Hint != "" {
			return api.SyncResponse{}, fmt.Errorf("control plane: %s (%s)", e.Error, e.Hint)
		}
		return api.SyncResponse{}, fmt.Errorf("control plane: %s", e.Error)
	}

	var out api.SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return api.SyncResponse{}, fmt.Errorf("control plane: malformed response: %w", err)
	}
	return out, nil
}

// loadAvg1 reads the one-minute load average. A missing or unreadable
// /proc/loadavg reports zero rather than failing the sync.
func loadAvg1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}
