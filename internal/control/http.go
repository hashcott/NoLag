package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"gamenolag/internal/api"
)

// defaultListenPort is the WireGuard port a relay is told to listen on.
const defaultListenPort = 51820

// maxObservationsPerReport bounds one batch. A capture session sees a handful of
// distinct servers, not thousands; a report far past this is a client sending
// something other than game destinations.
const maxObservationsPerReport = 500

// Backend is the part of Store the handlers use. Narrowed to an interface so
// handler tests need no database.
type Backend interface {
	RegisterRelay(ctx context.Context, in RegisterInput) (RegisterOutput, error)
	AuthenticateRelay(ctx context.Context, token string) (string, error)
	RecordStatus(ctx context.Context, relayID string, st api.RelayStatus) error
	RecordReachability(ctx context.Context, contributorKey, relayPubKey string, ok bool, detail string) error
	ActivateDevice(ctx context.Context, contributorKey, devicePubKey, fingerprint string) (string, []api.DeviceSummary, error)
	Session(ctx context.Context, contributorKey, devicePubKey string, mtu int) (api.SessionResponse, error)
	Profile(ctx context.Context) (api.ProfileResponse, error)
	ReleaseDevice(ctx context.Context, contributorKey, deviceID string) error
	RecordObservation(ctx context.Context, contributorKey, gameID, dstIP string, dstPort int) error
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

	// MTU handed to clients for their tunnel adapter.
	clientMTU int
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
		clientMTU:  1420,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/relay/register", s.handleRegister)
	mux.HandleFunc("/v1/relay/sync", s.handleSync)
	mux.HandleFunc("/v1/relay/reachability", s.handleReachability)
	mux.HandleFunc("/v1/activate", s.handleActivate)
	mux.HandleFunc("/v1/session", s.handleSession)
	mux.HandleFunc("/v1/profile", s.handleProfile)
	mux.HandleFunc("/v1/devices/", s.handleDevices)
	mux.HandleFunc("/v1/observations", s.handleObservations)
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

// clientAuth pulls the credentials a client sends on every call.
//
// The contributor key travels in a header rather than a query string, because a
// query string lands in access logs and browser history. The device public key
// identifies which of that key's devices is calling.
func clientAuth(r *http.Request) (key, devicePubKey string) {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		r.Header.Get("X-Device-Key")
}

func (s *server) handleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST", "")
		return
	}
	var req api.ActivateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed JSON body", "")
		return
	}
	if req.ContributorKey == "" || req.DevicePublicKey == "" {
		writeErr(w, http.StatusBadRequest, "contributor_key and device_public_key are required", "")
		return
	}
	if len(req.Fingerprint) > 100 {
		req.Fingerprint = req.Fingerprint[:100]
	}
	// Activation consumes a device slot and is key-bearing, so it is limited like
	// the other endpoints that take a contributor key.
	if !s.limit(w, r, req.ContributorKey) {
		return
	}

	id, held, err := s.b.ActivateDevice(r.Context(), req.ContributorKey, req.DevicePublicKey, req.Fingerprint)
	switch {
	case errors.Is(err, ErrSlotsFull):
		// Name the machines holding the slots. Telling somebody they are out of
		// slots without saying which of their own devices hold them leaves them
		// guessing at their own hardware.
		writeJSON(w, http.StatusConflict, api.SlotsFullResponse{
			Error:   "this key has no device slots left",
			Hint:    "release one of these devices, then activate again",
			Devices: held,
		})
		return
	case errors.Is(err, ErrUnknownKey):
		writeErr(w, http.StatusForbidden, "that contributor key is not valid",
			"check for typos; keys use no I, L, O or U")
		return
	case err != nil:
		log.Printf("activate: %v", err)
		writeErr(w, http.StatusInternalServerError, "activation failed", "")
		return
	}
	writeJSON(w, http.StatusOK, api.ActivateResponse{DeviceID: id})
}

func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET", "")
		return
	}
	key, devKey := clientAuth(r)
	if key == "" || devKey == "" {
		writeErr(w, http.StatusUnauthorized, "missing credentials",
			"send the contributor key as a bearer token and the device public key in X-Device-Key")
		return
	}
	sess, err := s.b.Session(r.Context(), key, devKey, s.clientMTU)
	if errors.Is(err, ErrUnknownKey) {
		writeErr(w, http.StatusForbidden, "this device is not activated on that key",
			"run activation again")
		return
	}
	if err != nil {
		log.Printf("session: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not build a session", "")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET", "")
		return
	}
	// Deliberately unauthenticated: the profile is a list of public game server
	// address ranges, published by the game vendors themselves. Guarding it would
	// protect nothing and would stop a client fetching routes before activation.
	prof, err := s.b.Profile(r.Context())
	if err != nil {
		log.Printf("profile: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not read the profile", "")
		return
	}
	writeJSON(w, http.StatusOK, prof)
}

func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "use DELETE", "")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/devices/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusBadRequest, "path must be /v1/devices/<device-id>", "")
		return
	}
	key, _ := clientAuth(r)
	if key == "" {
		writeErr(w, http.StatusUnauthorized, "missing credentials",
			"send the contributor key as a bearer token")
		return
	}
	if err := s.b.ReleaseDevice(r.Context(), key, id); errors.Is(err, ErrUnknownKey) {
		// Same answer whether the device does not exist or belongs to somebody
		// else, so this is not a way to enumerate device ids.
		writeErr(w, http.StatusForbidden, "that key does not own a device with that id", "")
		return
	} else if err != nil {
		log.Printf("release device: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not release the device", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleObservations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST", "")
		return
	}
	var req api.ObservationReport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed JSON body", "")
		return
	}
	if req.ContributorKey == "" || req.GameID == "" {
		writeErr(w, http.StatusBadRequest, "contributor_key and game_id are required", "")
		return
	}
	if len(req.Observations) > maxObservationsPerReport {
		writeErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("at most %d observations per report", maxObservationsPerReport), "")
		return
	}
	if !s.limit(w, r, req.ContributorKey) {
		return
	}

	out := api.ObservationAccepted{}
	for _, o := range req.Observations {
		// Validate here, not at build time. A malformed address stored now is a
		// malformed address someone has to explain later, and the profile builder
		// would silently discard it as unverified with no trace of where it came
		// from. Private and loopback space is rejected outright: a game server is
		// not on 10.0.0.0/8, so an address there means the capture attributed local
		// traffic to the game.
		addr, err := netip.ParseAddr(o.DstIP)
		if err != nil || !addr.Is4() || addr.IsPrivate() || addr.IsLoopback() ||
			addr.IsLinkLocalUnicast() || addr.IsMulticast() ||
			o.DstPort < 1 || o.DstPort > 65535 {
			out.Rejected++
			continue
		}
		err = s.b.RecordObservation(r.Context(), req.ContributorKey, req.GameID, addr.String(), o.DstPort)
		if errors.Is(err, ErrUnknownKey) {
			writeErr(w, http.StatusForbidden, "unknown or revoked contributor key", "")
			return
		}
		if err != nil {
			log.Printf("observation: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not store the report", "")
			return
		}
		out.Accepted++
	}
	writeJSON(w, http.StatusOK, out)
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
	if errors.Is(err, ErrTooManyRelays) {
		writeErr(w, http.StatusConflict, "this key already has the maximum number of relays",
			"remove a relay you no longer run, or ask for a second contributor key")
		return
	}
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
