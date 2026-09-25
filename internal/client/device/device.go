// Package device holds the two things the service must remember across reboots:
// this machine's WireGuard identity, and where its control plane is.
//
// The identity is generated once and kept. Regenerating it on every start would
// burn a device slot per reboot and leave the contributor's key looking, to the
// control plane, like it was being shared around.
package device

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// Key is one machine's WireGuard identity in the two encodings it is needed in:
// hex for wireguard-go's IPC, base64 for the control plane and for anything a
// person reads.
type Key struct {
	PrivateHex   string
	PublicBase64 string
}

// NewKey generates a Curve25519 keypair with WireGuard's clamping.
func NewKey() (Key, error) {
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		return Key{}, fmt.Errorf("device: generating a key: %w", err)
	}
	// WireGuard's clamping: clear the low three bits, clear the top bit, set the
	// second-highest. A key that skips this is accepted by the arithmetic and then
	// interoperates with nothing.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return Key{}, fmt.Errorf("device: deriving the public key: %w", err)
	}
	return Key{
		PrivateHex:   hex.EncodeToString(priv),
		PublicBase64: base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// LoadOrCreateKey reads the machine's key, generating and storing one the first
// time.
//
// The file holds the private key. On Windows the protection that counts is the
// ACL on the containing directory, set at install time — file mode alone means
// little there, so it is set for the sake of a developer running this on Linux
// and is not the defence.
func LoadOrCreateKey(path string) (Key, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		k, err := keyFromPrivateHex(strings.TrimSpace(string(raw)))
		if err != nil {
			// Refuse rather than silently regenerating. A corrupt key file that is
			// quietly replaced looks, from the control plane, like a second machine,
			// and the contributor loses a slot to a file nobody knew had rotted.
			return Key{}, fmt.Errorf("device: %s is not a usable key file: %w; "+
				"delete it to have a new identity generated, which consumes a device slot", path, err)
		}
		return k, nil
	case !os.IsNotExist(err):
		return Key{}, fmt.Errorf("device: reading %s: %w", path, err)
	}

	k, err := NewKey()
	if err != nil {
		return Key{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Key{}, fmt.Errorf("device: creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(k.PrivateHex+"\n"), 0o600); err != nil {
		return Key{}, fmt.Errorf("device: writing %s: %w", path, err)
	}
	return k, nil
}

func keyFromPrivateHex(s string) (Key, error) {
	priv, err := hex.DecodeString(s)
	if err != nil {
		return Key{}, fmt.Errorf("not hex: %w", err)
	}
	if len(priv) != 32 {
		return Key{}, fmt.Errorf("decoded to %d bytes, want 32", len(priv))
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return Key{}, fmt.Errorf("deriving the public key: %w", err)
	}
	return Key{PrivateHex: s, PublicBase64: base64.StdEncoding.EncodeToString(pub)}, nil
}

// Config is what the installer leaves behind for the service.
//
// It is a file rather than something the UI sends, because the pipe from the UI
// carries verbs and names nothing. A UI that could hand the service a control
// plane URL would be a UI that could point it at someone else's.
type Config struct {
	ControlURL     string `json:"control_url"`
	ContributorKey string `json:"contributor_key"`
	// Games maps a title to the process names that mean it is running. Left out,
	// the built-in list is used.
	Games []GameConfig `json:"games,omitempty"`
}

// GameConfig is one title and the processes that identify it.
type GameConfig struct {
	ID           string   `json:"id"`
	ProcessNames []string `json:"process_names"`
}

// LoadConfig reads the service's configuration.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("device: reading %s: %w; the installer writes this file", path, err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("device: %s is not valid configuration: %w", path, err)
	}
	if c.ControlURL == "" || c.ContributorKey == "" {
		return Config{}, fmt.Errorf("device: %s must set control_url and contributor_key", path)
	}
	return c, nil
}
