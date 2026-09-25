// Command gnl-profile builds a game profile from what contributors have
// reported and publishes it for the relays and clients to pull.
//
//	gnl-profile -dsn postgres://... -game pubg \
//	            -aws-regions ap-southeast-1,ap-northeast-1 \
//	            -azure-regions southeastasia,japaneast
//
// It runs on the control plane's machine, on demand or from cron. Nothing here
// touches a player's machine: the addresses come from reports already stored,
// and the published ranges come from what AWS, Azure and the routing table say
// about those addresses.
//
// It prints what it would publish and, without -publish, stops there. A profile
// is routed by every client, so seeing the diff before it ships is the default.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"strings"
	"time"

	"gamenolag/internal/control"
	"gamenolag/internal/profile"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("GNL_DSN"), "Postgres connection string (env GNL_DSN)")
	game := flag.String("game", "", "game id to build a profile for (required)")
	awsRegions := flag.String("aws-regions", "", "comma-separated AWS regions to accept")
	azureRegions := flag.String("azure-regions", "", "comma-separated Azure regions to accept")
	azureURL := flag.String("azure-url", "", "URL of the Azure Service Tags JSON; Microsoft "+
		"publishes it behind a dated filename, so there is no stable one to default to")
	asns := flag.String("asns", "", "comma-separated ASNs whose announced prefixes count as published")
	minReporters := flag.Int("min-reporters", 3, "independent contributors an address needs before it is a candidate")
	publish := flag.Bool("publish", false, "actually publish; without it, print what would be published and stop")
	flag.Parse()

	if *dsn == "" || *game == "" {
		log.Fatal("-dsn and -game are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	store, err := control.Open(ctx, *dsn)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()

	candidates, err := store.CandidateAddresses(ctx, *game, *minReporters)
	if err != nil {
		log.Fatalf("read candidates: %v", err)
	}
	if len(candidates) == 0 {
		log.Fatalf("no address for %q has been reported by %d independent contributors yet.\n"+
			"That is the normal state early on: keep capturing, and check with\n"+
			"  SELECT dst_ip, COUNT(DISTINCT key_hash) FROM observed_address\n"+
			"   WHERE game_id = '%s' GROUP BY dst_ip ORDER BY 2 DESC;",
			*game, *minReporters, *game)
	}

	var observed []netip.Addr
	for _, c := range candidates {
		if a, err := netip.ParseAddr(c); err == nil {
			observed = append(observed, a)
		}
	}
	fmt.Printf("%d candidate address(es) for %s, reported by at least %d contributors each\n",
		len(observed), *game, *minReporters)

	sources, err := gatherSources(*awsRegions, *azureRegions, *azureURL, *asns)
	if err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Printf("%d published range(s) to check them against\n", len(sources))

	res, err := profile.Build(observed, sources, profile.DefaultOptions())
	if err != nil {
		// Refusing is the designed behaviour when there is nothing to check
		// against: a profile built with no cross-check contains whatever happened
		// to be observed and looks finished.
		log.Fatalf("build: %v", err)
	}

	fmt.Printf("\nprofile: %d prefix(es), %d addresses, widened to /%d\n",
		len(res.CIDRs), res.TotalAddresses, res.AppliedWidth)
	for _, c := range res.CIDRs {
		fmt.Printf("  %s\n", c)
	}
	if len(res.Unverified) > 0 {
		fmt.Printf("\n%d address(es) matched no published range and were NOT added:\n", len(res.Unverified))
		for _, u := range res.Unverified {
			fmt.Printf("  %s\n", u)
		}
		fmt.Println("These are usually voice chat or a CDN. Adding them by hand means")
		fmt.Println("routing whatever else lives in the same block, so check before you do.")
	}

	if !*publish {
		fmt.Println("\nNothing published. Re-run with -publish once the list above looks right.")
		return
	}
	version, err := store.PublishProfile(ctx, res.CIDRs)
	if err != nil {
		log.Fatalf("publish: %v", err)
	}
	fmt.Printf("\npublished as version %d. Relays pick it up within one sync; clients on their next session.\n", version)
}

func gatherSources(awsRegions, azureRegions, azureURL, asns string) ([]profile.Source, error) {
	var out []profile.Source

	if awsRegions != "" {
		body, err := profile.HTTPFetcher(profile.AWSRangesURL)
		if err != nil {
			return nil, fmt.Errorf("fetch AWS ranges: %w", err)
		}
		s, err := profile.AWSSources(body, split(awsRegions))
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}

	if azureRegions != "" {
		if azureURL == "" {
			return nil, fmt.Errorf("-azure-regions needs -azure-url: Microsoft publishes the " +
				"Service Tags file behind a dated filename, so there is no stable URL to default to")
		}
		body, err := profile.HTTPFetcher(azureURL)
		if err != nil {
			return nil, fmt.Errorf("fetch Azure service tags: %w", err)
		}
		s, err := profile.AzureSources(body, split(azureRegions))
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}

	for _, a := range split(asns) {
		var asn int
		if _, err := fmt.Sscanf(a, "%d", &asn); err != nil {
			return nil, fmt.Errorf("bad ASN %q", a)
		}
		body, err := profile.HTTPFetcher(fmt.Sprintf(profile.RIPEStatURL, asn))
		if err != nil {
			return nil, fmt.Errorf("fetch AS%d prefixes: %w", asn, err)
		}
		s, err := profile.ASNSources(body, asn)
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}
	return out, nil
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
