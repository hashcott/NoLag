package control

import (
	"regexp"
	"strings"
	"testing"
)

var keyShape = regexp.MustCompile(`^GNL(-[0-9A-HJ-NP-Z]{4}){4}$`)

func TestNewContributorKeyShape(t *testing.T) {
	k, err := NewContributorKey()
	if err != nil {
		t.Fatal(err)
	}
	if !keyShape.MatchString(k) {
		t.Errorf("key %q does not match GNL-XXXX-XXXX-XXXX-XXXX in Crockford base32", k)
	}
}

func TestNewContributorKeyIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		k, err := NewContributorKey()
		if err != nil {
			t.Fatal(err)
		}
		if seen[k] {
			t.Fatalf("duplicate key generated after %d draws: %s", i, k)
		}
		seen[k] = true
	}
}

// Crockford base32 omits I, L, O and U so a key read aloud or copied by hand
// cannot be confused between 1/I/L and 0/O.
func TestNewContributorKeyAvoidsAmbiguousCharacters(t *testing.T) {
	for i := 0; i < 200; i++ {
		k, _ := NewContributorKey()
		// Only the random groups are constrained. The fixed "GNL-" prefix
		// legitimately contains an L, and nobody reads it back from memory in
		// isolation - checking the whole string would fail on every key ever
		// generated, which is what it did.
		random := strings.TrimPrefix(k, "GNL-")
		if strings.ContainsAny(random, "ILOU") {
			t.Fatalf("key %q has an ambiguous character in its random part", k)
		}
	}
}

func TestNewRelayTokenIsLongAndUnique(t *testing.T) {
	a, err := NewRelayToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewRelayToken()
	if a == b {
		t.Error("two relay tokens came out identical")
	}
	if len(a) < 32 {
		t.Errorf("relay token is %d characters, want at least 32", len(a))
	}
}

func TestHashIsStableAndDistinct(t *testing.T) {
	if Hash("GNL-AAAA-BBBB-CCCC-DDDD") != Hash("GNL-AAAA-BBBB-CCCC-DDDD") {
		t.Error("Hash is not stable for the same input")
	}
	if Hash("a") == Hash("b") {
		t.Error("Hash collided on two different inputs")
	}
	if len(Hash("a")) != 64 {
		t.Errorf("Hash returned %d characters, want 64 hex characters", len(Hash("a")))
	}
}

func TestHashDoesNotReturnTheSecret(t *testing.T) {
	secret := "GNL-AAAA-BBBB-CCCC-DDDD"
	if strings.Contains(Hash(secret), secret) {
		t.Error("Hash output contains the plaintext secret")
	}
}
