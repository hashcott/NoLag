package profile

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Fetcher retrieves a URL. Injected so the parsers can be tested against saved
// fixtures rather than the live internet — these files change shape over time,
// and a test that depends on today's copy stops being a test.
type Fetcher func(url string) ([]byte, error)

// HTTPFetcher fetches over the network with a timeout.
func HTTPFetcher(url string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("profile: %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

const (
	AWSRangesURL = "https://ip-ranges.amazonaws.com/ip-ranges.json"
	RIPEStatURL  = "https://stat.ripe.net/data/announced-prefixes/data.json?resource=AS%d"
)

// awsDoc is the shape of AWS's published ranges.
type awsDoc struct {
	Prefixes []struct {
		IPPrefix string `json:"ip_prefix"`
		Region   string `json:"region"`
		Service  string `json:"service"`
	} `json:"prefixes"`
}

// AWSSources parses AWS's ip-ranges.json, keeping the regions asked for.
//
// GLOBAL_ACCELERATOR is marked anycast: those blocks are shared by every
// customer of the service, so an address inside one must not be widened to it.
func AWSSources(body []byte, regions []string) ([]Source, error) {
	var doc awsDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("profile: parse AWS ranges: %w", err)
	}
	want := make(map[string]bool, len(regions))
	for _, r := range regions {
		want[r] = true
	}
	var out []Source
	regional := 0 // anycast alone is not a cross-check; see the check below
	for _, p := range doc.Prefixes {
		anycast := p.Service == "GLOBAL_ACCELERATOR"
		// An anycast block is kept whatever region it claims: GLOBAL is not in any
		// caller's region list, and dropping it would silently lose the very
		// addresses that need the anycast rule.
		if !anycast && !want[p.Region] {
			continue
		}
		// EC2 is where game servers live. The other services in this file are
		// managed endpoints that happen to share address space.
		if !anycast && p.Service != "EC2" {
			continue
		}
		pfx, err := netip.ParsePrefix(p.IPPrefix)
		if err != nil || !pfx.Addr().Is4() {
			continue
		}
		out = append(out, Source{Prefix: pfx, Region: p.Region, Anycast: anycast})
		if !anycast {
			regional++
		}
	}
	// Judged on the regional blocks, not the total. Anycast survives the region
	// filter by design, so a wrong region name would otherwise leave the
	// accelerator block behind and look like a working cross-check - while every
	// regional address observed was discarded as unverified and the profile came
	// out empty.
	if regional == 0 {
		return nil, fmt.Errorf("profile: AWS ranges held nothing for regions %v - "+
			"check the region names against the file rather than shipping an empty cross-check",
			regions)
	}
	return out, nil
}

// azureDoc is the shape of Azure's Service Tags file.
type azureDoc struct {
	Values []struct {
		Name       string `json:"name"`
		Properties struct {
			Region          string   `json:"region"`
			SystemService   string   `json:"systemService"`
			AddressPrefixes []string `json:"addressPrefixes"`
		} `json:"properties"`
	} `json:"values"`
}

// AzureSources parses Azure's Service Tags, keeping the regions asked for.
func AzureSources(body []byte, regions []string) ([]Source, error) {
	var doc azureDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("profile: parse Azure service tags: %w", err)
	}
	want := make(map[string]bool, len(regions))
	for _, r := range regions {
		want[strings.ToLower(r)] = true
	}
	var out []Source
	for _, v := range doc.Values {
		region := strings.ToLower(v.Properties.Region)
		if !want[region] {
			continue
		}
		// The bare regional tag, not a per-service one: a game server is an ordinary
		// VM, so it sits in the region's own block rather than under a managed
		// service's tag.
		if v.Properties.SystemService != "" {
			continue
		}
		for _, cidr := range v.Properties.AddressPrefixes {
			pfx, err := netip.ParsePrefix(cidr)
			if err != nil || !pfx.Addr().Is4() {
				continue
			}
			out = append(out, Source{Prefix: pfx, Region: region})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("profile: Azure service tags held nothing for regions %v", regions)
	}
	return out, nil
}

// ripeDoc is the shape of RIPEstat's announced-prefixes answer.
type ripeDoc struct {
	Data struct {
		Prefixes []struct {
			Prefix string `json:"prefix"`
		} `json:"prefixes"`
	} `json:"data"`
}

// ASNSources parses what an autonomous system announces to the global routing
// table.
//
// This covers publishers who run their own network rather than renting cloud —
// Riot Direct, for instance — where no cloud range file would ever match.
func ASNSources(body []byte, asn int) ([]Source, error) {
	var doc ripeDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("profile: parse RIPEstat answer: %w", err)
	}
	var out []Source
	for _, p := range doc.Data.Prefixes {
		pfx, err := netip.ParsePrefix(p.Prefix)
		if err != nil || !pfx.Addr().Is4() {
			continue
		}
		out = append(out, Source{Prefix: pfx, Region: fmt.Sprintf("as%d", asn)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("profile: AS%d announces no IPv4 prefix according to "+
			"RIPEstat - check the ASN rather than treating this as an empty cross-check", asn)
	}
	return out, nil
}
