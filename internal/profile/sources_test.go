package profile

import (
	"strings"
	"testing"
)

// Shapes taken from the real files, trimmed. Fixtures rather than live fetches:
// these documents change, and a test depending on today's copy stops being a test.
const awsFixture = `{"syncToken":"1","prefixes":[
 {"ip_prefix":"20.24.48.0/20","region":"ap-southeast-1","service":"EC2"},
 {"ip_prefix":"52.139.208.0/20","region":"ap-southeast-1","service":"EC2"},
 {"ip_prefix":"13.32.0.0/15","region":"ap-southeast-1","service":"CLOUDFRONT"},
 {"ip_prefix":"54.230.0.0/16","region":"eu-west-1","service":"EC2"},
 {"ip_prefix":"15.230.0.0/17","region":"GLOBAL","service":"GLOBAL_ACCELERATOR"},
 {"ip_prefix":"2600:1f00::/40","region":"ap-southeast-1","service":"EC2"}
]}`

func TestAWSSourcesKeepsOnlyEC2InTheRegionsAsked(t *testing.T) {
	got, err := AWSSources([]byte(awsFixture), []string{"ap-southeast-1"})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, s := range got {
		kept = append(kept, s.Prefix.String())
	}
	joined := strings.Join(kept, " ")

	if !strings.Contains(joined, "20.24.48.0/20") || !strings.Contains(joined, "52.139.208.0/20") {
		t.Errorf("kept = %v, want both ap-southeast-1 EC2 blocks", kept)
	}
	if strings.Contains(joined, "13.32.0.0/15") {
		t.Error("kept a CLOUDFRONT block; a game server is an ordinary instance, and " +
			"CDN space would drag unrelated traffic through the relay")
	}
	if strings.Contains(joined, "54.230.0.0/16") {
		t.Error("kept an eu-west-1 block that was not asked for")
	}
	if strings.Contains(joined, "2600:") {
		t.Error("kept an IPv6 prefix; the tunnel is IPv4 only")
	}
}

// An anycast block must survive the region filter. GLOBAL is in nobody's region
// list, and dropping it would lose exactly the addresses the anycast rule exists
// for.
func TestAWSSourcesKeepsAnycastRegardlessOfRegion(t *testing.T) {
	got, err := AWSSources([]byte(awsFixture), []string{"ap-southeast-1"})
	if err != nil {
		t.Fatal(err)
	}
	var found *Source
	for i := range got {
		if got[i].Prefix.String() == "15.230.0.0/17" {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatal("the GLOBAL_ACCELERATOR block was filtered out by region; an address " +
			"inside it would then match nothing and be discarded as unverified")
	}
	if !found.Anycast {
		t.Error("the accelerator block is not marked anycast, so an address in it " +
			"would be widened to a /17 shared by every customer of the service")
	}
}

// Shipping an empty cross-check is worse than failing: every observed address
// would be discarded as unverified and the profile would come out empty while
// looking like it worked.
func TestAWSSourcesRefusesWhenNothingMatches(t *testing.T) {
	_, err := AWSSources([]byte(awsFixture), []string{"no-such-region"})
	if err == nil {
		t.Fatal("want an error when the region names match nothing")
	}
	if !strings.Contains(err.Error(), "no-such-region") {
		t.Errorf("the error does not name the regions asked for: %v", err)
	}
}

const azureFixture = `{"values":[
 {"name":"SoutheastAsia","properties":{"region":"southeastasia","systemService":"",
   "addressPrefixes":["20.24.0.0/16","52.139.192.0/18","2603:1040::/47"]}},
 {"name":"Storage.SoutheastAsia","properties":{"region":"southeastasia","systemService":"AzureStorage",
   "addressPrefixes":["20.150.0.0/17"]}},
 {"name":"JapanEast","properties":{"region":"japaneast","systemService":"",
   "addressPrefixes":["20.43.64.0/18"]}}
]}`

func TestAzureSourcesTakesTheRegionalTagNotTheServiceTags(t *testing.T) {
	got, err := AzureSources([]byte(azureFixture), []string{"southeastasia"})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, s := range got {
		kept = append(kept, s.Prefix.String())
	}
	joined := strings.Join(kept, " ")

	if !strings.Contains(joined, "20.24.0.0/16") {
		t.Errorf("kept = %v, want the regional block", kept)
	}
	if strings.Contains(joined, "20.150.0.0/17") {
		t.Error("kept AzureStorage space; a game server is an ordinary VM and sits " +
			"in the region's own block, not under a managed service tag")
	}
	if strings.Contains(joined, "20.43.64.0/18") {
		t.Error("kept japaneast, which was not asked for")
	}
	if strings.Contains(joined, "2603:") {
		t.Error("kept an IPv6 prefix")
	}
}

func TestAzureRegionMatchIsCaseInsensitive(t *testing.T) {
	got, err := AzureSources([]byte(azureFixture), []string{"SoutheastAsia"})
	if err != nil {
		t.Fatalf("region names in the wild vary in case; this must not fail: %v", err)
	}
	if len(got) == 0 {
		t.Error("no sources for SoutheastAsia")
	}
}

const ripeFixture = `{"data":{"prefixes":[
 {"prefix":"151.106.248.0/24"},
 {"prefix":"104.160.128.0/20"},
 {"prefix":"2a02:26f0::/32"}
]}}`

// Publishers who run their own network - Riot Direct, for instance - match no
// cloud range file at all, so the ASN is the only cross-check available.
func TestASNSourcesParsesAnnouncedPrefixes(t *testing.T) {
	got, err := ASNSources([]byte(ripeFixture), 6507)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d prefixes, want the two IPv4 ones", len(got))
	}
	if got[0].Region != "as6507" {
		t.Errorf("region = %q, want as6507 so the origin is visible in the profile", got[0].Region)
	}
}

func TestASNSourcesRefusesAnEmptyAnswer(t *testing.T) {
	_, err := ASNSources([]byte(`{"data":{"prefixes":[]}}`), 6507)
	if err == nil {
		t.Fatal("want an error: an empty answer means the ASN is wrong, not that the " +
			"publisher announces nothing")
	}
	if !strings.Contains(err.Error(), "6507") {
		t.Errorf("the error does not name the ASN: %v", err)
	}
}

func TestSourcesRejectMalformedJSON(t *testing.T) {
	if _, err := AWSSources([]byte("not json"), []string{"x"}); err == nil {
		t.Error("AWSSources accepted malformed JSON")
	}
	if _, err := AzureSources([]byte("not json"), []string{"x"}); err == nil {
		t.Error("AzureSources accepted malformed JSON")
	}
	if _, err := ASNSources([]byte("not json"), 1); err == nil {
		t.Error("ASNSources accepted malformed JSON")
	}
}
