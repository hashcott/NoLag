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
//
// Two assumptions this relies on, stated because neither is visible at the call
// site. It writes the record and its newline in a single Write, and the kernel
// serializes O_APPEND writes to the same inode, so concurrent cron runs on ONE
// machine cannot interleave a line — but O_APPEND is not atomic across clients
// on NFS, so the results path must be local, not network-mounted. And 0o644
// means only the owning UID can append: if cron and a hand-run operator use
// different accounts, the second one gets EACCES rather than a silent problem.
//
// The mode values 0o755 and 0o644 are requests masked by the process umask,
// so a hardened cron umask can produce files the intended human readers cannot read.
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

// Load reads every record from a results file, returning the records and the
// number of lines it could not parse.
//
// A missing file is an empty campaign, not an error.
//
// An unparseable line is skipped rather than fatal. Append fsyncs every record,
// so a completed line survives a crash — but a machine that dies mid-write still
// leaves a torn final line, and that is the most likely damage a week-long
// unattended campaign will ever have. Failing the whole file for it would throw
// away six good days to punish one bad line. The skip count is returned rather
// than logged so the caller can say so out loud; silently dropping data from a
// measurement tool is how a wrong verdict gets believed.
func Load(path string) ([]Record, int, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	var out []Record
	skipped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			skipped++
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		// A scan error is different from a bad line: the file itself could not be
		// read to the end, so what was parsed is an unknown fraction of the whole.
		// Hand back what we have AND the error, and let the caller decide.
		return out, skipped, err
	}
	return out, skipped, nil
}
