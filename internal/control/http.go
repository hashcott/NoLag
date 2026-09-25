package control

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"gamenolag/internal/api"
)

// defaultListenPort is the WireGuard port a relay is told to listen on.
const defaultListenPort = 51820

// Backend is the part of Store the handlers use. Narrowed to an interface so
// handler tests need no database.
type Backend interface {
	RegisterRelay(ctx context.Context, in RegisterInput) (RegisterOutput, error)
	AuthenticateRelay(ctx context.Context, token string) (string, error)
	RecordStatus(ctx context.Context, relayID string, st api.RelayStatus) error
	RecordReachability(ctx context.Context, contributorKey, relayPubKey string, ok bool, detail string) error
	DesiredState(ctx context.Context, relayID string) ([]api.Peer, []string, error)
}

type server struct {
	b        Backend
	pollSecs int

	// Spec 10.2. Both limiters guard the endpoints that consume a contributor
	// key, and they run before the store is touched - so they also bound the
	// other damage a flood does, which is burning values from the relay subnet
	// sequence that never recycles.
	byIP  *Limiter
	byKey *Limiter

	// Set only when something in front of this really does set X-Forwarded-For.
	trustProxy bool
}

// NewServer returns the control plane's HTTP handler.
func NewServer(b Backend, pollSecs int, trustProxy bool) http.Handler {
	s := &server{
		b:        b,
		pollSecs: pollSecs,
		// A contributor registers once, and re-runs the installer a handful of
		// times at worst. Twenty an hour is far above honest use and far below
		// anything useful for probing.
		byIP:  NewLimiter(20, time.Hour),
		byKey: NewLimiter(20, time.Hour),

		trustProxy: trustProxy,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/relay/register", s.handleRegister)
	mux.HandleFunc("/v1/relay/sync", s.handleSync)
	mux.HandleFunc("/v1/relay/reachability", s.handleReachability)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	})
	return mux
}

func (s *server) handleReachability(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST", "")
		return
	}
	var req api.ReachabilityReport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed JSON body", "")
		return
	}
	if req.ContributorKey == "" || req.RelayPublicKey == "" {
		writeErr(w, http.StatusBadRequest, "contributor_key and relay_public_key are required", "")
		return
	}
	// Cap it: this string is shown back to a human and stored.
	if len(req.Detail) > 500 {
		req.Detail = req.Detail[:500]
	}

	if !s.limit(w, r, req.ContributorKey) {
		return
	}

	err := s.b.RecordReachability(r.Context(), req.ContributorKey, req.RelayPublicKey, req.Reachable, req.Detail)
	if errors.Is(err, ErrUnknownKey) {
		writeErr(w, http.StatusForbidden, "that key does not own a relay with that public key",
			"you can only report on relays you contributed")
		return
	}
	if err != nil {
		log.Printf("reachability: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not record the result", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg, hint string) {
	writeJSON(w, code, api.ErrorResponse{Error: msg, Hint: hint})
}

// clientIP is the address to rate limit against.
//
// X-Forwarded-For is honoured only when the control plane is told it sits behind
// a proxy. Trusting it unconditionally would hand every client its own rate limit
// bucket, chosen by the client - which is not a rate limit at all.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i > 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limit applies both buckets and answers 429 when either is exhausted.
//
// The reply says nothing about which bucket tripped or how much budget is left:
// that would tell someone probing the key space how to pace themselves.
func (s *server) limit(w http.ResponseWriter, r *http.Request, contributorKey string) bool {
	ipOK := s.byIP.Allow(clientIP(r, s.trustProxy))
	keyOK := true
	if contributorKey != "" {
		keyOK = s.byKey.Allow(KeyPrefix(contributorKey))
	}
	if ipOK && keyOK {
		return true
	}
	w.Header().Set("Retry-After", "3600")
	writeErr(w, http.StatusTooManyRequests, "too many requests",
		"wait an hour and try again; if you are setting up a relay and hit this, "+
			"you are re-running the installer more than expected")
	return false
}

func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST", "")
		return
	}
	var req api.RegisterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed JSON body", "")
		return
	}
	switch {
	case req.ContributorKey == "":
		writeErr(w, http.StatusBadRequest, "contributor_key is required", "")
		return
	case req.PublicKey == "":
		writeErr(w, http.StatusBadRequest, "public_key is required", "")
		return
	case req.Endpoint == "":
		writeErr(w, http.StatusBadRequest, "endpoint is required", "")
		return
	}
	if _, _, err := net.SplitHostPort(req.Endpoint); err != nil {
		writeErr(w, http.StatusBadRequest, "endpoint must be host:port",
			"for example 203.0.113.10:51820")
		return
	}
	if !s.limit(w, r, req.ContributorKey) {
		return
	}

	out, err := s.b.RegisterRelay(r.Context(), RegisterInput{
		ContributorKey: req.ContributorKey,
		PublicKey:      req.PublicKey,
		Endpoint:       req.Endpoint,
		Region:         req.Region,
		Hostname:       req.Hostname,
	})
	if errors.Is(err, ErrUnknownKey) {
		// Deliberately vague: an attacker probing key space learns only that this
		// one did not work, not whether it once existed or was revoked.
		writeErr(w, http.StatusForbidden, "that contributor key is not valid",
			"check for typos; keys use no I, L, O or U")
		return
	}
	if err != nil {
		log.Printf("register: %v", err)
		writeErr(w, http.StatusInternalServerError, "registration failed", "")
		return
	}

	writeJSON(w, http.StatusOK, api.RegisterResponse{
		RelayID:     out.RelayID,
		RelayToken:  out.RelayToken,
		InnerSubnet: out.InnerSubnet,
		InnerIP:     out.InnerIP,
		ListenPort:  defaultListenPort,
	})
}

func (s *server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST", "")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	relayID, err := s.b.AuthenticateRelay(r.Context(), token)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid relay token",
			"re-run the installer to register again and get a new token")
		return
	}

	var req api.SyncRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed JSON body", "")
		return
	}

	if err := s.b.RecordStatus(r.Context(), relayID, req.Status); err != nil {
		// The status report is telemetry. Losing it must not cost the relay its
		// desired state, which is the half that keeps players connected.
		log.Printf("sync: record status for %s: %v", relayID, err)
	}

	peers, cidrs, err := s.b.DesiredState(r.Context(), relayID)
	if err != nil {
		log.Printf("sync: desired state for %s: %v", relayID, err)
		writeErr(w, http.StatusInternalServerError, "could not read desired state", "")
		return
	}

	// Validate here rather than trusting the database. A peer with a wide
	// AllowedIPs would be handed to the kernel by the agent, and could then
	// receive another client's return traffic.
	valid := make([]api.Peer, 0, len(peers))
	for _, p := range peers {
		if err := p.Validate(); err != nil {
			log.Printf("sync: dropping invalid peer for %s: %v", relayID, err)
			continue
		}
		valid = append(valid, p)
	}

	writeJSON(w, http.StatusOK, api.SyncResponse{
		Peers: valid, GameCIDRs: cidrs, PollSecs: s.pollSecs,
	})
}
