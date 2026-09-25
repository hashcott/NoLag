package ipsetsync

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormaliseSortsAndDedupes(t *testing.T) {
	got := Normalise([]string{"52.139.208.0/20", "20.24.48.0/20", "52.139.208.0/20", "  20.24.48.0/20  ", ""})
	want := []string{"20.24.48.0/20", "52.139.208.0/20"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Normalise = %v, want %v", got, want)
	}
}

func TestFingerprintIgnoresOrder(t *testing.T) {
	a := Fingerprint([]string{"a/32", "b/32"})
	b := Fingerprint([]string{"b/32", "a/32"})
	if a != b {
		t.Errorf("fingerprints differ for the same set in a different order: %s vs %s", a, b)
	}
}

func TestFingerprintChangesWithContent(t *testing.T) {
	if Fingerprint([]string{"a/32"}) == Fingerprint([]string{"a/32", "b/32"}) {
		t.Error("fingerprint did not change when a CIDR was added")
	}
}

func TestSyncAppliesOnFirstCall(t *testing.T) {
	s := New("gnl-games")
	calls := 0
	var gotSet string
	var gotCIDRs []string
	apply := func(setName string, sorted []string) error {
		calls++
		gotSet = setName
		gotCIDRs = sorted
		return nil
	}
	if err := s.Sync([]string{"b/32", "a/32"}, apply); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("apply called %d times, want 1", calls)
	}
	if gotSet != "gnl-games" {
		t.Errorf("set name = %q, want gnl-games", gotSet)
	}
	if !reflect.DeepEqual(gotCIDRs, []string{"a/32", "b/32"}) {
		t.Errorf("apply received %v, want sorted [a/32 b/32]", gotCIDRs)
	}
}

func TestSyncSkipsWhenUnchanged(t *testing.T) {
	s := New("gnl-games")
	calls := 0
	apply := func(string, []string) error { calls++; return nil }

	s.Sync([]string{"a/32", "b/32"}, apply)
	s.Sync([]string{"b/32", "a/32"}, apply) // same set, different order
	s.Sync([]string{"a/32", "b/32"}, apply)

	if calls != 1 {
		t.Errorf("apply called %d times, want 1: an unchanged list must not be reapplied", calls)
	}
}

func TestSyncReappliesWhenChanged(t *testing.T) {
	s := New("gnl-games")
	calls := 0
	apply := func(string, []string) error { calls++; return nil }

	s.Sync([]string{"a/32"}, apply)
	s.Sync([]string{"a/32", "b/32"}, apply)

	if calls != 2 {
		t.Errorf("apply called %d times, want 2", calls)
	}
}

// A failed apply must not be recorded, or a transient ipset failure would leave
// the relay permanently out of date while Sync reports success.
func TestSyncRetriesAfterFailure(t *testing.T) {
	s := New("gnl-games")
	calls := 0
	boom := errors.New("ipset unavailable")
	apply := func(string, []string) error {
		calls++
		if calls == 1 {
			return boom
		}
		return nil
	}

	if err := s.Sync([]string{"a/32"}, apply); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the apply error", err)
	}
	if err := s.Sync([]string{"a/32"}, apply); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if calls != 2 {
		t.Errorf("apply called %d times, want 2: a failed apply must be retried", calls)
	}
}

// An empty list is a meaningful state: it means nothing may be forwarded.
func TestSyncAppliesEmptyList(t *testing.T) {
	s := New("gnl-games")
	calls := 0
	apply := func(_ string, sorted []string) error {
		calls++
		if len(sorted) != 0 {
			t.Errorf("apply received %v, want an empty list", sorted)
		}
		return nil
	}
	if err := s.Sync(nil, apply); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("apply called %d times, want 1", calls)
	}
}
