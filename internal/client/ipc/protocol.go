// Package ipc is the boundary between the user-facing app and the service that
// holds every privilege.
//
// The UI runs as an ordinary user; the service runs as LocalSystem because
// creating a Wintun adapter demands it — running the UI "as administrator" is
// not enough and fails with access denied. So this pipe is the only place where
// something unprivileged asks something all-powerful to act, and it is the whole
// attack surface between them.
//
// It therefore accepts exactly four verbs and no parameters that name anything.
// No file path, no endpoint, no CIDR, no command. Everything the service acts on
// comes from the profile and session it fetched itself over TLS. A UI that has
// been tampered with can ask for a connect it was going to ask for anyway; it
// cannot point the service at an attacker's relay or make it write a file.
package ipc

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Verb is one of the four things the UI may ask for.
type Verb string

const (
	VerbConnect       Verb = "connect"
	VerbDisconnect    Verb = "disconnect"
	VerbStatus        Verb = "status"
	VerbReloadProfile Verb = "reload-profile"
)

// Request is one line of JSON from the UI.
//
// It carries a verb and nothing else. Adding a field here means deciding what a
// compromised UI may make LocalSystem do with it, so the bar for a new field is
// deliberately high.
type Request struct {
	Verb Verb `json:"verb"`
}

// Response is one line of JSON back.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	// Status fields, present on a status reply.
	State        string  `json:"state,omitempty"`
	RelayID      string  `json:"relay_id,omitempty"`
	GameRunning  string  `json:"game_running,omitempty"`
	ActiveRoutes int     `json:"active_routes,omitempty"`
	TunnelRTTms  float64 `json:"tunnel_rtt_ms,omitempty"`
	LossPct      float64 `json:"loss_pct,omitempty"`
}

// ErrUnknownVerb is returned for anything outside the four.
var ErrUnknownVerb = fmt.Errorf("ipc: unknown verb")

// ErrNoService means the service could not be reached: it is not running, or
// the caller is not admitted by the pipe's access control.
//
// Here rather than beside the pipe itself because the interface decides what to
// say about it, and that decision is not Windows-specific.
var ErrNoService = fmt.Errorf("ipc: the GameNoLag service is not reachable")

// ParseRequest decodes one line and accepts it only if it names one of the four
// verbs.
//
// Unknown verbs are refused rather than ignored. Silently dropping one would let
// a future version of the UI believe it had asked for something, and would hide
// the case this check exists to catch: something other than the UI talking to
// the pipe.
func ParseRequest(line []byte) (Request, error) {
	// Bound it. The pipe is reachable by any process the ACL admits, and a reader
	// that will accept an arbitrarily long line is a way to exhaust a service
	// running as LocalSystem.
	if len(line) > MaxLineBytes {
		return Request{}, fmt.Errorf("ipc: request larger than %d bytes", MaxLineBytes)
	}
	var r Request
	dec := json.NewDecoder(strings.NewReader(string(line)))
	// Reject unknown fields rather than ignoring them: a request carrying
	// something this version does not understand is not a request this version
	// should act on.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Request{}, fmt.Errorf("ipc: malformed request: %w", err)
	}
	switch r.Verb {
	case VerbConnect, VerbDisconnect, VerbStatus, VerbReloadProfile:
		return r, nil
	default:
		return Request{}, fmt.Errorf("%w: %q", ErrUnknownVerb, r.Verb)
	}
}

// MaxLineBytes bounds one request. Four verbs need a few dozen bytes; anything
// approaching this is not the UI.
const MaxLineBytes = 4096

// Encode serialises a response as one line.
func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Refuse builds the response for a rejected request.
//
// The text says what is wrong without describing the service's state, because
// the caller has not been established as the real UI at this point.
func Refuse(err error) Response {
	return Response{OK: false, Error: err.Error()}
}
