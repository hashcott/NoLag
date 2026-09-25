package profile

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

// A profile built with nothing to check against would contain whatever happened
// to be observed, look finished, and route a player's traffic somewhere nobody
// chose. Refusing is the correct outcome.
func TestBuildRefusesWithNoSources(t *testing.T) {
	_, err := Build(addrs("20.24.48.10"), nil, DefaultOptions())
	if !errors.Is(err, ErrNoSources) {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}

// An address matching no published range is never added. In practice these turn
// out to be voice chat or a CDN, and absorbing them would route those too.
func TestUnmatchedAddressesAreSetAsideNotAdded(t *testing.T) {
	sources := []Source{{Prefix: netip.MustParsePrefix("20.24.48.0/20"), Region: "sgp"}}
	got, err := Build(addrs("20.24.48.10", "198.51.100.7"), sources, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.CIDRs {
		p := netip.MustParsePrefix(c)
		if p.Contains(netip.MustParseAddr("198.51.100.7")) {
			t.Errorf("profile %v covers an address matching no source", got.CIDRs)
		}
	}
	if len(got.Unverified) != 1 || got.Unverified[0] != "198.51.100.7" {
		t.Errorf("Unverified = %v, want the one unmatched address", got.Unverified)
	}
}

// An anycast block is shared by every customer of the service, so the published
// range says nothing about who is behind this particular address.
func TestAnycastIsKeptAsASingleAddress(t *testing.T) {
	sources := []Source{{
		Prefix:  netip.MustParsePrefix("15.230.0.0/17"),
		Region:  "global",
		Anycast: true,
	}}
	got, err := Build(addrs("15.230.1.2"), sources, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CIDRs) != 1 || got.CIDRs[0] != "15.230.1.2/32" {
		t.Errorf("CIDRs = %v, want the single address; widening an anycast block "+
			"would route every customer of that service", got.CIDRs)
	}
}

// If the source published a /24, routing a /20 around it invents coverage
// nobody stated.
func TestNeverWiderThanTheSourcePublished(t *testing.T) {
	sources := []Source{{Prefix: netip.MustParsePrefix("151.106.248.0/24"), Region: "sgp"}}
	got, err := Build(addrs("151.106.248.9"), sources, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got.CIDRs[0] != "151.106.248.0/24" {
		t.Errorf("CIDRs = %v, want the published /24 and no wider", got.CIDRs)
	}
}

// The per-prefix cap alone is not enough: many prefixes at the cap still add up.
func TestTotalCapNarrowsTheWholeBuild(t *testing.T) {
	// Forty separate /16 sources, so each observation could widen to a /20.
	var sources []Source
	var obs []netip.Addr
	for i := 0; i < 40; i++ {
		sources = append(sources, Source{
			Prefix: netip.MustParsePrefix(netip.AddrFrom4([4]byte{20, byte(i), 0, 0}).String() + "/16"),
			Region: "sgp",
		})
		obs = append(obs, netip.AddrFrom4([4]byte{20, byte(i), 5, 9}))
	}
	opt := DefaultOptions() // /20 ceiling, 131072 total
	got, err := Build(obs, sources, opt)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalAddresses > opt.MaxTotalAddresses {
		t.Errorf("profile covers %d addresses, over the %d cap", got.TotalAddresses, opt.MaxTotalAddresses)
	}
	if got.AppliedWidth <= opt.MaxPrefixWidth {
		t.Errorf("AppliedWidth = %d; the build should have narrowed past %d to fit",
			got.AppliedWidth, opt.MaxPrefixWidth)
	}
}

// When even the floor cannot fit the cap, the build returns at the floor rather
// than narrowing past it, and reports a total above the cap so the caller can
// see the ceiling was not honoured. Narrowing past /24 would mean routing
// individual addresses, which is a different product.
func TestNarrowingStopsAtTheFloorAndSaysSo(t *testing.T) {
	// 600 distinct /24s: 600 * 256 = 153,600 addresses, over the 131,072 cap even
	// at the narrowest width allowed.
	var sources []Source
	var obs []netip.Addr
	for i := 0; i < 600; i++ {
		b2, b3 := byte(i/256), byte(i%256)
		sources = append(sources, Source{
			Prefix: netip.PrefixFrom(netip.AddrFrom4([4]byte{20, b2, b3, 0}), 24).Masked(),
			Region: "sgp",
		})
		obs = append(obs, netip.AddrFrom4([4]byte{20, b2, b3, 9}))
	}
	opt := DefaultOptions()
	got, err := Build(obs, sources, opt)
	if err != nil {
		t.Fatal(err)
	}
	if got.AppliedWidth != opt.NarrowestPrefixWidth {
		t.Errorf("AppliedWidth = %d, want the floor %d", got.AppliedWidth, opt.NarrowestPrefixWidth)
	}
	if got.TotalAddresses <= opt.MaxTotalAddresses {
		t.Errorf("TotalAddresses = %d; this fixture is meant to exceed the %d cap, "+
			"so the test is not exercising the floor", got.TotalAddresses, opt.MaxTotalAddresses)
	}
}

func TestCompactMergesSiblingsAndDropsCovered(t *testing.T) {
	in := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/24"),
		netip.MustParsePrefix("10.0.1.0/24"), // sibling of the first -> /23
		netip.MustParsePrefix("10.0.2.0/24"),
		netip.MustParsePrefix("10.0.3.0/24"), // sibling -> /23, then both -> /22
		netip.MustParsePrefix("192.168.5.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"), // covers the line above
	}
	got := Compact(in)
	var s []string
	for _, p := range got {
		s = append(s, p.String())
	}
	joined := strings.Join(s, " ")
	if !strings.Contains(joined, "10.0.0.0/22") {
		t.Errorf("Compact = %v, want the four /24s merged to 10.0.0.0/22", s)
	}
	if strings.Contains(joined, "192.168.5.0/24") {
		t.Errorf("Compact = %v, kept a prefix already covered by 192.168.0.0/16", s)
	}
	if !strings.Contains(joined, "192.168.0.0/16") {
		t.Errorf("Compact = %v, lost the covering prefix", s)
	}
}

// Two prefixes that merely sort next to each other are not siblings. Merging
// them would cover addresses nobody observed.
func TestCompactDoesNotMergeNonSiblings(t *testing.T) {
	in := []netip.Prefix{
		netip.MustParsePrefix("10.0.1.0/24"), // upper half of 10.0.0.0/23
		netip.MustParsePrefix("10.0.2.0/24"), // lower half of 10.0.2.0/23
	}
	got := Compact(in)
	if len(got) != 2 {
		t.Errorf("Compact = %v, want both kept: they are adjacent but not siblings", got)
	}
}

// Once two games' ranges are mixed there is no way to tell them apart again, and
// one game's routes would be installed whenever the other was running.
func TestOverlapsDetectsAnotherGamesAddresses(t *testing.T) {
	hits := Overlaps([]string{"20.24.48.0/20"}, addrs("20.24.50.5"))
	if len(hits) != 1 {
		t.Fatalf("hits = %v, want one", hits)
	}
	if !strings.Contains(hits[0], "20.24.50.5") {
		t.Errorf("hit does not name the offending address: %s", hits[0])
	}
	if len(Overlaps([]string{"20.24.48.0/20"}, addrs("198.51.100.1"))) != 0 {
		t.Error("reported an overlap for an address outside the profile")
	}
}

// The narrowest containing source wins: a published /24 says more about who is
// behind an address than the /17 that also contains it.
func TestNarrowestSourceWins(t *testing.T) {
	sources := []Source{
		{Prefix: netip.MustParsePrefix("20.0.0.0/8"), Region: "wide"},
		{Prefix: netip.MustParsePrefix("20.24.48.0/24"), Region: "narrow"},
	}
	got, err := Build(addrs("20.24.48.7"), sources, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got.CIDRs[0] != "20.24.48.0/24" {
		t.Errorf("CIDRs = %v, want the narrow source to win", got.CIDRs)
	}
}
