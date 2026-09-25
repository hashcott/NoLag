package wintun

import "testing"

// Two genuine 32-byte keys, in the base64 the control plane speaks.
const (
	keyA = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	keyB = "ZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXp7fH1+f4CBgoM="
)

func TestBase64ToHexRoundTripsARealKey(t *testing.T) {
	got, err := base64ToHex(keyA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Fatalf("hex key is %d characters, want 64", len(got))
	}
	if got != "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("hex = %s", got)
	}
}

func TestAWrongSizedKeyIsRefused(t *testing.T) {
	// Accepting one would present as a relay that never answers, sending the user
	// to look at their network for a fault in their configuration.
	for _, k := range []string{
		"TGlzdGVuIHRvIHRoZSBieXRlcyBoZXJlIG9rYXk=", // 29 bytes
		"",
		"AAEC", // 3 bytes
		"not base64 at all !!",
	} {
		if _, err := base64ToHex(k); err == nil {
			t.Errorf("accepted %q as a 32-byte key", k)
		}
	}
}

func TestHandshakeIsAttributedToTheRightPeer(t *testing.T) {
	hexA, _ := base64ToHex(keyA)
	hexB, _ := base64ToHex(keyB)
	dump := "private_key=00\n" +
		"public_key=" + hexA + "\nlast_handshake_time_sec=0\nlast_handshake_time_nsec=0\n" +
		"public_key=" + hexB + "\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=250000000\n"

	if got := handshakeStamp(dump, keyA); got != "" {
		t.Errorf("peer A never handshook but reported %q", got)
	}
	if got := handshakeStamp(dump, keyB); got != "1700000000.250000000" {
		t.Errorf("peer B stamp = %q", got)
	}
}

func TestStampChangesWithinTheSameSecond(t *testing.T) {
	// Second granularity alone would make two measurements a few hundred
	// milliseconds apart look like no answer at all.
	hexA, _ := base64ToHex(keyA)
	first := "public_key=" + hexA + "\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=100000000\n"
	second := "public_key=" + hexA + "\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=900000000\n"
	if handshakeStamp(first, keyA) == handshakeStamp(second, keyA) {
		t.Fatal("two handshakes in the same second gave the same stamp")
	}
}

func TestAnAbsentPeerHasNoStamp(t *testing.T) {
	hexA, _ := base64ToHex(keyA)
	dump := "public_key=" + hexA + "\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=1\n"
	if got := handshakeStamp(dump, keyB); got != "" {
		t.Fatalf("a peer that is not in the dump reported %q", got)
	}
}

func TestAMalformedKeyNeverMatchesAPeer(t *testing.T) {
	if got := handshakeStamp("public_key=ffff\nlast_handshake_time_sec=1\n", "not base64"); got != "" {
		t.Fatalf("a malformed key matched a peer: %q", got)
	}
}

func TestParseStampRejectsWhatNeverHandshook(t *testing.T) {
	for _, s := range []string{"", "0.0", "notanumber.0", "-5.0"} {
		if _, ok := parseStamp(s); ok {
			t.Errorf("parseStamp(%q) reported a handshake", s)
		}
	}
}

func TestParseStampKeepsTheNanoseconds(t *testing.T) {
	ts, ok := parseStamp("1700000000.250000000")
	if !ok {
		t.Fatal("a real stamp was rejected")
	}
	if ts.Unix() != 1700000000 || ts.Nanosecond() != 250000000 {
		t.Fatalf("stamp = %v", ts)
	}
}
