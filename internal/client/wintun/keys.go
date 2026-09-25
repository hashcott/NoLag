package wintun

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// base64ToHex converts a WireGuard key from the encoding the control plane and
// people use to the one wireguard-go's IPC wants.
//
// The length is checked rather than assumed: a key that is the wrong size is
// accepted by the encoding and then interoperates with nothing, which presents
// as a handshake that never completes — indistinguishable, from the client, from
// a relay that is down.
func base64ToHex(key string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		return "", fmt.Errorf("not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("decoded to %d bytes, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// handshakeStamp reads one peer's last handshake time out of an IPC dump, as an
// opaque string that changes when a handshake completes.
//
// Both the second and nanosecond fields, because the second alone has one-second
// granularity: re-measuring a relay twice inside the same second would show an
// unchanged stamp and be read as no answer at all.
//
// It returns "" when the peer is absent or has never completed a handshake.
func handshakeStamp(ipcDump, publicKeyBase64 string) string {
	wantHex, err := base64ToHex(publicKeyBase64)
	if err != nil {
		return ""
	}
	var inPeer bool
	var sec, nsec string
	for _, line := range strings.Split(ipcDump, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "public_key":
			// A dump lists every peer in turn, so the key line is what decides which
			// peer the following fields belong to. Reading a handshake off the wrong
			// peer would pick a relay on another relay's timing.
			inPeer = strings.EqualFold(v, wantHex)
		case "last_handshake_time_sec":
			if inPeer {
				sec = v
			}
		case "last_handshake_time_nsec":
			if inPeer {
				nsec = v
			}
		}
	}
	if sec == "" || (sec == "0" && (nsec == "0" || nsec == "")) {
		return ""
	}
	return sec + "." + nsec
}

// parseStamp turns a handshake stamp back into a time. Ok is false when the peer
// has never completed one.
func parseStamp(stamp string) (time.Time, bool) {
	sec, nsec, ok := strings.Cut(stamp, ".")
	if !ok {
		return time.Time{}, false
	}
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || s <= 0 {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(nsec, 10, 64)
	if err != nil {
		n = 0
	}
	return time.Unix(s, n), true
}
