// Package cloud talks to the control plane on behalf of the Windows client.
//
// Everything the service acts on arrives through here. The pipe from the UI
// carries four verbs and names nothing, so this is the only place a relay
// endpoint, a public key or a routable prefix can enter the service — which is
// why the transport is checked rather than assumed.
package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gamenolag/internal/api"
)

// Client is one client's view of the control plane.
type Client struct {
	// BaseURL must be https, except against a loopback address for development.
	BaseURL string
	// ContributorKey identifies the person; DevicePublicKey identifies the machine.
	ContributorKey  string
	DevicePublicKey string

	HTTP *http.Client
}

// ErrNotActivated means the control plane does not recognise this device on this
// key. The service recovers by activating, not by retrying.
var ErrNotActivated = fmt.Errorf("cloud: this device is not activated")

// SlotsFullError carries the machines already holding this key's slots.
//
// The list is the point: telling somebody they are out of slots without naming
// their own hardware leaves them guessing at which machine to release.
type SlotsFullError struct {
	Devices []api.DeviceSummary
	Hint    string
}

func (e *SlotsFullError) Error() string {
	names := make([]string, 0, len(e.Devices))
	for _, d := range e.Devices {
		names = append(names, d.Fingerprint)
	}
	return fmt.Sprintf("cloud: no device slots left; held by: %s", strings.Join(names, ", "))
}

// maxBody bounds any response. The largest thing the control plane returns is
// the profile, a few thousand prefixes at roughly twenty bytes each.
const maxBody = 1 << 20

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// checkURL refuses a base URL that would put the contributor key on the wire in
// clear. A config file is an editable file on a machine other people may touch,
// so this is checked every call rather than trusted once.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("cloud: control plane URL %q is not a URL: %w", raw, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if u.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return nil // development against a control plane on this machine
	}
	return fmt.Errorf("cloud: control plane URL %q is not https; the contributor key "+
		"would travel in clear", raw)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, auth bool) error {
	if err := checkURL(c.BaseURL); err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.ContributorKey)
		req.Header.Set("X-Device-Key", c.DevicePublicKey)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("cloud: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("cloud: %s %s: reading the reply: %w", method, path, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("cloud: %s %s: reply was not the expected JSON: %w", method, path, err)
		}
		return nil
	case http.StatusConflict:
		var full api.SlotsFullResponse
		if json.Unmarshal(raw, &full) == nil && len(full.Devices) > 0 {
			return &SlotsFullError{Devices: full.Devices, Hint: full.Hint}
		}
	case http.StatusForbidden:
		return ErrNotActivated
	}

	var e api.ErrorResponse
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		if e.Hint != "" {
			return fmt.Errorf("cloud: %s %s: %s (%s)", method, path, e.Error, e.Hint)
		}
		return fmt.Errorf("cloud: %s %s: %s", method, path, e.Error)
	}
	return fmt.Errorf("cloud: %s %s: control plane answered %s", method, path, resp.Status)
}

// Activate claims a device slot for this machine's key.
func (c *Client) Activate(ctx context.Context, fingerprint string) (api.ActivateResponse, error) {
	var out api.ActivateResponse
	err := c.do(ctx, http.MethodPost, "/v1/activate", api.ActivateRequest{
		ContributorKey:  c.ContributorKey,
		DevicePublicKey: c.DevicePublicKey,
		Fingerprint:     fingerprint,
	}, &out, false)
	return out, err
}

// Session asks which relays this device may use right now.
func (c *Client) Session(ctx context.Context) (api.SessionResponse, error) {
	var out api.SessionResponse
	err := c.do(ctx, http.MethodGet, "/v1/session", nil, &out, true)
	return out, err
}

// Profile fetches the published game ranges.
//
// Unauthenticated on purpose at the server, and sent without credentials here to
// match: it is a list of public address ranges, and a client that cannot fetch
// it has no routes to install at all.
func (c *Client) Profile(ctx context.Context) (api.ProfileResponse, error) {
	var out api.ProfileResponse
	err := c.do(ctx, http.MethodGet, "/v1/profile", nil, &out, false)
	return out, err
}
