package device

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestNewKeyIsClampedAndDerives(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, err := hex.DecodeString(k.PrivateHex)
	if err != nil || len(priv) != 32 {
		t.Fatalf("private key is not 32 hex bytes: %v", err)
	}
	// Without clamping the key is accepted by the arithmetic and interoperates
	// with nothing, which presents as a handshake that never completes.
	if priv[0]&7 != 0 {
		t.Errorf("low three bits not cleared: %08b", priv[0])
	}
	if priv[31]&128 != 0 {
		t.Errorf("top bit not cleared: %08b", priv[31])
	}
	if priv[31]&64 == 0 {
		t.Errorf("second-highest bit not set: %08b", priv[31])
	}
	pub, err := base64.StdEncoding.DecodeString(k.PublicBase64)
	if err != nil || len(pub) != 32 {
		t.Fatalf("public key is not 32 base64 bytes: %v", err)
	}
}

func TestKeyIsKeptAcrossRestarts(t *testing.T) {
	// A key regenerated on every start burns a device slot per reboot.
	path := filepath.Join(t.TempDir(), "sub", "device.key")
	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identity changed across restarts:\n%+v\n%+v", first, second)
	}
}

func TestACorruptKeyFileIsRefusedNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.key")
	if err := os.WriteFile(path, []byte("not a key at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("a corrupt key file was silently replaced, costing a device slot")
	}
}

func TestAShortKeyFileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.key")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(make([]byte, 16))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("a 16-byte key was accepted")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigNeedsBothFields(t *testing.T) {
	for _, body := range []string{
		`{"control_url":"https://api.example.com"}`,
		`{"contributor_key":"GNL-ABCD"}`,
		`{}`,
	} {
		if _, err := LoadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("accepted incomplete config %s", body)
		}
	}
}

func TestConfigRejectsUnknownFields(t *testing.T) {
	// A field this version does not understand is a field somebody expected to
	// take effect. Ignoring it silently is how a machine ends up not doing what
	// its configuration says.
	body := `{"control_url":"https://api.example.com","contributor_key":"GNL-ABCD","relay_endpoint":"evil:51820"}`
	if _, err := LoadConfig(writeConfig(t, body)); err == nil {
		t.Fatal("an unknown configuration field was ignored")
	}
}

func TestConfigReadsGames(t *testing.T) {
	body := `{"control_url":"https://api.example.com","contributor_key":"GNL-ABCD",
	          "games":[{"id":"pubg","process_names":["TslGame.exe"]}]}`
	c, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Games) != 1 || c.Games[0].ID != "pubg" || c.Games[0].ProcessNames[0] != "TslGame.exe" {
		t.Fatalf("games = %+v", c.Games)
	}
}
