package probe

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gamenolag/internal/stats"
)

// Record is one measurement run, written as one JSON object per line.
//
// Leg names the segment being measured, per the design document section 11.4:
//
//	A  player -> game landmark   (the baseline: the ISP's own route)
//	B  player -> candidate VPS   (the first leg of the tunnel)
//	C  candidate VPS -> landmark (the second leg of the tunnel)
//
// The tunnel is worth building only where B + C < A.
type Record struct {
	TS     time.Time `json:"ts"`
	Leg    string    `json:"leg"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Target string    `json:"target"`
	stats.Summary
}

// Append adds one record to the results file, creating parent directories and
// the file itself if they do not exist.
//
// Append, not rewrite: a week-long campaign must survive the machine rebooting,
// the process being killed, and cron overlapping two runs.
func Append(path string, r Record) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Load reads every record from a results file. A missing file is an empty
// campaign, not an error.
func Load(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}
