// Package profile turns addresses observed carrying a game's traffic into the
// narrow list of prefixes the client routes into the tunnel.
//
// This is the part of the product that is genuinely hard. Routing works on
// destination address, so the profile has to say which addresses belong to the
// game — and it has to be narrow. AWS and Azure publish the enclosing blocks as
// /17s and /18s; routing one of those would drag thousands of unrelated services
// through a relay that exists to carry game packets.
//
// Everything here is pure: no network, no files. The sources are fetched
// elsewhere and handed in, so the arithmetic that decides what a player's
// machine will route can be tested exhaustively.
package profile

import (
	"fmt"
	"net/netip"
	"sort"
)

// Options controls how far an observed address may be widened.
type Options struct {
	// MaxPrefixWidth is the widest prefix an observed address may be widened to,
	// as a prefix length. 20 means /20 is allowed and /19 is not.
	MaxPrefixWidth int
	// NarrowestPrefixWidth is the floor the automatic narrowing stops at.
	NarrowestPrefixWidth int
	// MaxTotalAddresses caps the whole profile. Reached, the builder rebuilds one
	// bit narrower and tries again. A per-prefix cap alone is not enough: fifty
	// /20s is still an eighth of a /12.
	MaxTotalAddresses uint64
}

// DefaultOptions are the values the design document fixes.
func DefaultOptions() Options {
	return Options{
		MaxPrefixWidth:       20,
		NarrowestPrefixWidth: 24,
		MaxTotalAddresses:    131072,
	}
}

// Source is a published range an observed address may be matched against —
// an AWS or Azure block, or a prefix an ASN announces.
type Source struct {
	Prefix netip.Prefix
	Region string
	// Anycast marks a block shared by every customer of a service, such as AWS
	// Global Accelerator. A match inside one is kept as a single address and
	// never widened: the published block says nothing about who is behind it.
	Anycast bool
}

// Result is a built profile plus everything deliberately left out of it.
type Result struct {
	CIDRs []string
	// Unverified are observed addresses matching no published source. They are
	// never added automatically. In practice they turn out to be voice chat or a
	// CDN, and a profile that quietly absorbed them would route those too.
	Unverified []string
	// AppliedWidth is the prefix width the build settled on after narrowing.
	AppliedWidth int
	// TotalAddresses the profile covers.
	TotalAddresses uint64
}

// ErrNoSources is returned when there is nothing to check observations against.
//
// Refusing is the point. A profile built with no cross-check would contain
// whatever happened to be observed, look finished, and route a player's traffic
// somewhere nobody chose.
var ErrNoSources = fmt.Errorf("profile: no published sources to check observations against")

// Build turns observed addresses into a profile.
func Build(observed []netip.Addr, sources []Source, opt Options) (Result, error) {
	if len(sources) == 0 {
		return Result{}, ErrNoSources
	}

	var matched []match
	var unverified []string
	for _, a := range observed {
		if !a.Is4() {
			continue
		}
		if m, ok := matchAddr(a, sources); ok {
			matched = append(matched, m)
		} else {
			unverified = append(unverified, a.String())
		}
	}
	sort.Strings(unverified)

	// Widen at the requested width; if the whole profile is too large, rebuild one
	// bit narrower and try again. Narrowing the whole build rather than trimming
	// the largest prefixes keeps the result explainable: every prefix in it was
	// produced by the same rule.
	for width := opt.MaxPrefixWidth; width <= opt.NarrowestPrefixWidth; width++ {
		prefixes := widenAll(matched, width)
		prefixes = Compact(prefixes)
		total := countAddresses(prefixes)
		if total <= opt.MaxTotalAddresses || width == opt.NarrowestPrefixWidth {
			out := make([]string, 0, len(prefixes))
			for _, p := range prefixes {
				out = append(out, p.String())
			}
			return Result{
				CIDRs:          out,
				Unverified:     unverified,
				AppliedWidth:   width,
				TotalAddresses: total,
			}, nil
		}
	}
	return Result{}, fmt.Errorf("profile: unreachable")
}

type match struct {
	addr    netip.Addr
	source  Source
	matched bool
}

func matchAddr(a netip.Addr, sources []Source) (match, bool) {
	// The narrowest containing source wins: a published /24 tells us more about
	// who is behind an address than the /17 that also contains it.
	best := -1
	for i, s := range sources {
		if !s.Prefix.Contains(a) {
			continue
		}
		if best == -1 || s.Prefix.Bits() > sources[best].Prefix.Bits() {
			best = i
		}
	}
	if best == -1 {
		return match{}, false
	}
	return match{addr: a, source: sources[best], matched: true}, true
}

func widenAll(ms []match, width int) []netip.Prefix {
	var out []netip.Prefix
	for _, m := range ms {
		out = append(out, widen(m, width))
	}
	return out
}

// widen takes one matched address to the prefix that will be routed for it.
func widen(m match, width int) netip.Prefix {
	if m.source.Anycast {
		// Never widened. An anycast block is shared by every customer of the
		// service, so the published range says nothing about who is behind this
		// particular address - widening it would route all of them.
		return netip.PrefixFrom(m.addr, 32)
	}
	// Never wider than the source actually published. If AWS says this is a /24,
	// routing a /20 around it invents coverage nobody stated.
	bits := m.source.Prefix.Bits()
	if bits < width {
		bits = width
	}
	return netip.PrefixFrom(m.addr, bits).Masked()
}

// Compact removes prefixes covered by a wider one and merges siblings.
//
// Both matter on a player's machine, where every prefix is a routing table
// entry installed and removed each time the game starts and stops.
func Compact(in []netip.Prefix) []netip.Prefix {
	uniq := make(map[netip.Prefix]bool, len(in))
	for _, p := range in {
		uniq[p.Masked()] = true
	}
	list := make([]netip.Prefix, 0, len(uniq))
	for p := range uniq {
		list = append(list, p)
	}
	sortPrefixes(list)

	// Repeat until it settles: merging two /24s into a /23 can make that /23 the
	// sibling of another, and one pass would stop short.
	for {
		list = dropCovered(list)
		merged, changed := mergeSiblings(list)
		list = merged
		if !changed {
			return list
		}
	}
}

func dropCovered(list []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for i, p := range list {
		covered := false
		for j, q := range list {
			if i == j || q.Bits() >= p.Bits() {
				continue // only a strictly wider prefix can contain this one
			}
			if q.Overlaps(p) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

func mergeSiblings(list []netip.Prefix) ([]netip.Prefix, bool) {
	sortPrefixes(list)
	var out []netip.Prefix
	changed := false
	for i := 0; i < len(list); i++ {
		if i+1 < len(list) {
			a, b := list[i], list[i+1]
			if a.Bits() == b.Bits() && a.Bits() > 0 {
				parent := netip.PrefixFrom(a.Addr(), a.Bits()-1).Masked()
				// Siblings only: both halves must sit under the same parent, and the
				// first must be the lower half. Otherwise two unrelated prefixes that
				// merely sort next to each other would be merged into a block covering
				// addresses nobody observed.
				if parent.Contains(a.Addr()) && parent.Contains(b.Addr()) && a.Masked() == a && parent.Addr() == a.Addr() {
					out = append(out, parent)
					i++
					changed = true
					continue
				}
			}
		}
		out = append(out, list[i])
	}
	return out, changed
}

func sortPrefixes(list []netip.Prefix) {
	sort.Slice(list, func(i, j int) bool {
		if c := list[i].Addr().Compare(list[j].Addr()); c != 0 {
			return c < 0
		}
		return list[i].Bits() < list[j].Bits()
	})
}

func countAddresses(list []netip.Prefix) uint64 {
	var n uint64
	for _, p := range list {
		n += uint64(1) << uint(32-p.Bits())
	}
	return n
}

// Overlaps reports whether a profile would cover any of another game's observed
// addresses.
//
// One game's profile must never cover another's traffic: once two games' ranges
// are mixed there is no way to tell them apart again, and the routes for one
// would be installed whenever the other was running.
func Overlaps(cidrs []string, otherObserved []netip.Addr) []string {
	var hits []string
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		for _, a := range otherObserved {
			if p.Contains(a) {
				hits = append(hits, fmt.Sprintf("%s contains %s", c, a))
				break
			}
		}
	}
	return hits
}
