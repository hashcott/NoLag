// Package ipsetsync keeps the relay's egress allowlist in step with the game
// CIDRs the control plane publishes.
//
// The allowlist lives in an ipset rather than in individual iptables rules:
// iptables walks its chains linearly, so a profile of several hundred CIDRs
// would cost CPU on every forwarded packet. An ipset is a hash lookup, and one
// iptables rule references it.
package ipsetsync

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"sort"
	"strings"
)

// Normalise trims, drops blanks, sorts and dedupes a CIDR list so that two
// equivalent lists compare equal.
func Normalise(cidrs []string) []string {
	seen := make(map[string]bool, len(cidrs))
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Fingerprint is a stable hash of the normalised list.
func Fingerprint(cidrs []string) string {
	h := sha256.New()
	for _, c := range Normalise(cidrs) {
		h.Write([]byte(c))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Syncer remembers the last list it applied successfully.
//
// Remembering what we applied, rather than reading the kernel back, keeps the
// hot path free of shell-outs: the allowlist changes when a profile is published,
// which is rare, while the poll runs every ten seconds.
type Syncer struct {
	setName string
	applied string // fingerprint of the last successful apply; empty until one succeeds
}

// New returns a Syncer for the named ipset. The zero applied fingerprint means
// the first Sync after an agent restart always applies, which is what recovers
// from a set that was destroyed or edited while the agent was down.
func New(setName string) *Syncer {
	return &Syncer{setName: setName}
}

// Sync calls apply only when the list differs from the last successful apply.
//
// A failed apply is not recorded, so the next call retries. Recording it would
// leave the relay permanently out of date while Sync kept reporting success.
func (s *Syncer) Sync(cidrs []string, apply func(setName string, sorted []string) error) error {
	fp := Fingerprint(cidrs)
	if fp == s.applied {
		return nil
	}
	if err := apply(s.setName, Normalise(cidrs)); err != nil {
		return err
	}
	s.applied = fp
	return nil
}

// ApplyWithIpset rebuilds the set under a temporary name and swaps it into place.
//
// The swap is atomic: there is no instant where the live set is half-built and
// packets that should be forwarded are dropped. It is also why the set is
// rebuilt wholesale rather than edited entry by entry.
func ApplyWithIpset(setName string, sorted []string) error {
	tmp := setName + "-new"

	_ = exec.Command("ipset", "destroy", tmp).Run() // ignore: usually "set does not exist"

	if out, err := exec.Command("ipset", "create", tmp, "hash:net", "family", "inet").CombinedOutput(); err != nil {
		return cmdErr("ipset create", out, err)
	}
	for _, c := range sorted {
		if out, err := exec.Command("ipset", "add", tmp, c).CombinedOutput(); err != nil {
			_ = exec.Command("ipset", "destroy", tmp).Run()
			return cmdErr("ipset add "+c, out, err)
		}
	}
	// The live set must exist before it can be swapped into; creating it here
	// makes the first run on a fresh machine work without a separate setup step.
	if out, err := exec.Command("ipset", "create", setName, "hash:net", "family", "inet", "-exist").CombinedOutput(); err != nil {
		_ = exec.Command("ipset", "destroy", tmp).Run()
		return cmdErr("ipset create "+setName, out, err)
	}
	if out, err := exec.Command("ipset", "swap", tmp, setName).CombinedOutput(); err != nil {
		_ = exec.Command("ipset", "destroy", tmp).Run()
		return cmdErr("ipset swap", out, err)
	}
	if out, err := exec.Command("ipset", "destroy", tmp).CombinedOutput(); err != nil {
		return cmdErr("ipset destroy "+tmp, out, err)
	}
	return nil
}

func cmdErr(what string, out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return err
	}
	return &CommandError{What: what, Output: msg, Err: err}
}

// CommandError carries the command's own output, because ipset explains itself
// on stderr and the exit code alone names the wrong cause.
type CommandError struct {
	What   string
	Output string
	Err    error
}

func (e *CommandError) Error() string { return e.What + ": " + e.Output }
func (e *CommandError) Unwrap() error { return e.Err }
