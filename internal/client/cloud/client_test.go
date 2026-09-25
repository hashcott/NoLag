package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"gamenolag/internal/api"
)

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{
		BaseURL:         srv.URL, // loopback, so the https rule allows it
		ContributorKey:  "GNL-ABCD-EFGH",
		DevicePublicKey: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		HTTP:            srv.Client(),
	}
}

func TestPlainHTTPToARemoteHostIsRefused(t *testing.T) {
	c := &Client{BaseURL: "http://control.example.com", ContributorKey: "GNL-ABCD"}
	_, err := c.Session(context.Background())
	if err == nil {
		t.Fatal("sent the contributor key to a plain-http remote host")
	}
	// The failure must name the reason, because the fix is editing a config file.
	if got := err.Error(); !contains(got, "https") {
		t.Fatalf("error does not say why: %q", got)
	}
}

func TestLoopbackHTTPIsAllowedForDevelopment(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.SessionResponse{ProfileVersion: 7})
	})
	sess, err := c.Session(context.Background())
	if err != nil {
		t.Fatalf("loopback refused: %v", err)
	}
	if sess.ProfileVersion != 7 {
		t.Fatalf("profile version = %d, want 7", sess.ProfileVersion)
	}
}

func TestSessionSendsBothCredentials(t *testing.T) {
	var gotAuth, gotDev string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotDev = r.Header.Get("Authorization"), r.Header.Get("X-Device-Key")
		json.NewEncoder(w).Encode(api.SessionResponse{})
	})
	if _, err := c.Session(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+c.ContributorKey {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotDev != c.DevicePublicKey {
		t.Fatalf("X-Device-Key = %q", gotDev)
	}
}

func TestProfileIsFetchedWithoutCredentials(t *testing.T) {
	// The service fetches the profile before it has ever activated. Sending
	// credentials it may not have yet would turn a first run into a failure.
	var sawAuth bool
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != "" || r.Header.Get("X-Device-Key") != ""
		json.NewEncoder(w).Encode(api.ProfileResponse{Version: 3, CIDRs: []string{"20.24.48.0/20"}})
	})
	prof, err := c.Profile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sawAuth {
		t.Fatal("profile request carried credentials")
	}
	if prof.Version != 3 || len(prof.CIDRs) != 1 {
		t.Fatalf("profile = %+v", prof)
	}
}

func TestForbiddenBecomesErrNotActivated(t *testing.T) {
	// The service must be able to tell "activate me" apart from every other
	// failure, because the recovery is different: activating, not retrying.
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(api.ErrorResponse{Error: "this device is not activated on that key"})
	})
	_, err := c.Session(context.Background())
	if !errors.Is(err, ErrNotActivated) {
		t.Fatalf("err = %v, want ErrNotActivated", err)
	}
}

func TestSlotsFullCarriesTheDevicesHoldingThem(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(api.SlotsFullResponse{
			Error: "this key has no device slots left",
			Hint:  "release one of these devices",
			Devices: []api.DeviceSummary{
				{ID: "d1", Fingerprint: "GAMING-PC"},
				{ID: "d2", Fingerprint: "LAPTOP"},
			},
		})
	})
	_, err := c.Activate(context.Background(), "NEW-PC")
	var full *SlotsFullError
	if !errors.As(err, &full) {
		t.Fatalf("err = %v, want SlotsFullError", err)
	}
	if len(full.Devices) != 2 {
		t.Fatalf("carried %d devices, want 2", len(full.Devices))
	}
	if msg := full.Error(); !contains(msg, "GAMING-PC") || !contains(msg, "LAPTOP") {
		t.Fatalf("message does not name the machines: %q", msg)
	}
}

func TestServerErrorTextReachesTheCaller(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(api.ErrorResponse{Error: "could not build a session", Hint: "try later"})
	})
	_, err := c.Session(context.Background())
	if err == nil {
		t.Fatal("a 500 was reported as success")
	}
	if !contains(err.Error(), "could not build a session") || !contains(err.Error(), "try later") {
		t.Fatalf("error lost the server's words: %q", err)
	}
}

func TestAnOversizedReplyDoesNotExhaustTheService(t *testing.T) {
	// The service runs as LocalSystem. A control plane that has been replaced, or
	// anything terminating TLS in front of it, must not be able to make it read
	// without limit.
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := make([]byte, 64<<10)
		for i := range chunk {
			chunk[i] = 'x'
		}
		w.Write([]byte(`{"cidrs":["`))
		for i := 0; i < 40; i++ { // 2.5 MB, past the 1 MB bound
			w.Write(chunk)
		}
		w.Write([]byte(`"]}`))
	})
	if _, err := c.Profile(context.Background()); err == nil {
		t.Fatal("a reply past the bound was accepted")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
