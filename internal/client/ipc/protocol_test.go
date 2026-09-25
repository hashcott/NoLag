package ipc

import (
	"errors"
	"strings"
	"testing"
)

func TestAcceptsExactlyTheFourVerbs(t *testing.T) {
	for _, v := range []Verb{VerbConnect, VerbDisconnect, VerbStatus, VerbReloadProfile} {
		r, err := ParseRequest([]byte(`{"verb":"` + string(v) + `"}`))
		if err != nil {
			t.Errorf("%q was refused: %v", v, err)
		}
		if r.Verb != v {
			t.Errorf("parsed %q as %q", v, r.Verb)
		}
	}
}

// The pipe is the only place something unprivileged asks LocalSystem to act.
// Anything outside the four is refused, not ignored: silently dropping one would
// hide exactly the case this check exists for.
func TestRefusesEverythingElse(t *testing.T) {
	for _, bad := range []string{
		`{"verb":"shutdown"}`,
		`{"verb":"connect-now"}`,
		`{"verb":""}`,
		`{"verb":"CONNECT"}`, // case matters; this is a protocol, not a prompt
		`{}`,
	} {
		if _, err := ParseRequest([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// A request naming a path, an endpoint or a command must not parse. The service
// acts only on what it fetched itself; accepting these fields is how a tampered
// UI would point LocalSystem somewhere.
func TestRefusesRequestsCarryingParameters(t *testing.T) {
	for _, bad := range []string{
		`{"verb":"connect","relay":"attacker.example.com:51820"}`,
		`{"verb":"reload-profile","path":"C:\\Users\\Public\\evil.json"}`,
		`{"verb":"connect","cidrs":["0.0.0.0/0"]}`,
		`{"verb":"status","exec":"cmd.exe"}`,
	} {
		if _, err := ParseRequest([]byte(bad)); err == nil {
			t.Errorf("accepted a request carrying parameters: %s", bad)
		}
	}
}

func TestUnknownVerbIsDistinguishable(t *testing.T) {
	_, err := ParseRequest([]byte(`{"verb":"nope"}`))
	if !errors.Is(err, ErrUnknownVerb) {
		t.Errorf("err = %v, want ErrUnknownVerb so the service can log it as such", err)
	}
}

// A reader that accepts an arbitrarily long line is a way to exhaust a service
// running as LocalSystem.
func TestBoundsTheRequestSize(t *testing.T) {
	huge := `{"verb":"status","x":"` + strings.Repeat("A", MaxLineBytes) + `"}`
	if _, err := ParseRequest([]byte(huge)); err == nil {
		t.Error("accepted a request over the size bound")
	}
}

func TestRefusesMalformedJSON(t *testing.T) {
	for _, bad := range []string{`not json`, `{"verb":`, ``, `[]`} {
		if _, err := ParseRequest([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestEncodeProducesOneLine(t *testing.T) {
	b, err := Encode(Response{OK: true, State: "connected", ActiveRoutes: 12})
	if err != nil {
		t.Fatal(err)
	}
	if b[len(b)-1] != '\n' {
		t.Error("response is not newline-terminated; the protocol is one message per line")
	}
	if strings.Count(string(b), "\n") != 1 {
		t.Errorf("response spans more than one line: %q", b)
	}
}

// The refusal text must not describe what the service is doing: at that point
// the caller has not been established as the real UI.
func TestRefusalDoesNotLeakState(t *testing.T) {
	r := Refuse(ErrUnknownVerb)
	if r.OK {
		t.Error("a refusal is marked OK")
	}
	if r.State != "" || r.RelayID != "" || r.ActiveRoutes != 0 {
		t.Errorf("refusal carries service state: %+v", r)
	}
}
