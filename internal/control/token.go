package control

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// crockford is base32 without I, L, O or U, so a key that is read aloud, written
// down or retyped cannot be confused between 1/I/L or 0/O.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewContributorKey returns a key of the form GNL-XXXX-XXXX-XXXX-XXXX.
//
// Sixteen characters from a 32-symbol alphabet is 80 bits. The key is a
// credential a person copies by hand, so it trades some entropy for being
// typeable; brute-forcing 80 bits through a rate-limited endpoint is not a
// threat worth more characters.
func NewContributorKey() (string, error) {
	groups := make([]string, 4)
	for g := range groups {
		var sb strings.Builder
		for i := 0; i < 4; i++ {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(crockford))))
			if err != nil {
				return "", fmt.Errorf("control: generate key: %w", err)
			}
			sb.WriteByte(crockford[n.Int64()])
		}
		groups[g] = sb.String()
	}
	return "GNL-" + strings.Join(groups, "-"), nil
}

// NewRelayToken returns a 256-bit random bearer token, hex encoded.
//
// Unlike a contributor key this is never typed by a person: the installer
// writes it to a file. So it is as long as it should be.
func NewRelayToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("control: generate relay token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Hash returns the hex SHA-256 of a secret, for storage and for indexed lookup.
//
// Not Argon2id or bcrypt, deliberately. Those exist to slow down guessing a
// human-chosen password. These secrets are 80 and 256 random bits, so there is
// nothing to guess, and a salted slow KDF cannot be looked up by index — every
// request would have to scan the table. SHA-256 keeps the property that matters
// (no plaintext secret at rest) and stays an O(1) lookup.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Equal compares two hashes in constant time.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
