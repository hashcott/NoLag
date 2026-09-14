package probe

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gamenolag/internal/stats"
)

func TestAppendAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")

	r1 := Record{
		TS:     time.Date(2026, 9, 14, 20, 0, 0, 0, time.UTC),
		Leg:    "B",
		From:   "vn-viettel",
		To:     "vps-sgp-vultr",
		Target: "203.0.113.10:51830",
		Summary: stats.Summary{
			Sent: 600, Received: 599, LossPct: 0.1667,
			P50Ms: 28.4, P95Ms: 31.2, P99Ms: 40.1, JitterMs: 2.8,
		},
	}
	r2 := r1
	r2.Leg = "C"
	r2.From = "vps-sgp-vultr"
	r2.To = "landmark-sgp"

	if err := Append(path, r1); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, r2); err != nil {
		t.Fatal(err)
	}

	got, skipped, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d records, want 2", len(got))
	}
	if got[0].Leg != "B" || got[1].Leg != "C" {
		t.Errorf("legs = %q, %q; want B, C", got[0].Leg, got[1].Leg)
	}
	if got[0].P50Ms != 28.4 {
		t.Errorf("P50Ms = %v, want 28.4", got[0].P50Ms)
	}
	if !got[0].TS.Equal(r1.TS) {
		t.Errorf("TS = %v, want %v", got[0].TS, r1.TS)
	}
}

func TestAppendCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "results.jsonl")
	if err := Append(path, Record{Leg: "A", TS: time.Now()}); err != nil {
		t.Fatalf("Append must create the directory and file: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file was not created: %v", err)
	}
}

func TestLoadSkipsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	if err := Append(path, Record{Leg: "A", TS: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("\n\n")
	f.Close()

	got, skipped, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(got) != 1 {
		t.Errorf("loaded %d records, want 1", len(got))
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	got, skipped, err := Load(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("a missing results file is an empty campaign, not an error: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(got) != 0 {
		t.Errorf("loaded %d records, want 0", len(got))
	}
}

func TestLoadSurvivesATornFinalLine(t *testing.T) {
	// The realistic crash artifact: the machine died part-way through writing the
	// last record. Everything before it must still load.
	path := filepath.Join(t.TempDir(), "results.jsonl")
	for i := 0; i < 3; i++ {
		if err := Append(path, Record{Leg: "A", From: "vn", TS: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"ts":"2026-09-14T13:00:00Z","leg":"B","fr`) // torn mid-object, no newline
	f.Close()

	got, skipped, err := Load(path)
	if err != nil {
		t.Fatalf("a torn final line must not fail the load: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("loaded %d records, want 3: the good records before the torn line "+
			"must survive", len(got))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1: the damage must be reported, not hidden", skipped)
	}
}

func TestLoadSkipsGarbageLineInMiddle(t *testing.T) {
	// A garbage line in the middle of the file should not stop the load, but
	// records on both sides of it must survive and the skip must be reported.
	path := filepath.Join(t.TempDir(), "results.jsonl")
	if err := Append(path, Record{Leg: "A", From: "vn", TS: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Write a garbage line directly to the file
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("this is not valid json\n")
	f.Close()

	if err := Append(path, Record{Leg: "B", From: "sgp", TS: time.Now()}); err != nil {
		t.Fatal(err)
	}

	got, skipped, err := Load(path)
	if err != nil {
		t.Fatalf("a garbage line in the middle must not fail the load: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("loaded %d records, want 2: records before and after garbage must survive", len(got))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1: the garbage line must be reported", skipped)
	}
}
