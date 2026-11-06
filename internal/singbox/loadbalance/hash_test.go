package loadbalance

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

func TestSiteKey(t *testing.T) {
	address := M.ParseSocksaddr("203.0.113.7:443")
	for _, item := range []struct {
		name        string
		destination M.Socksaddr
		metadata    *adapter.InboundContext
		want        string
	}{
		{"registrable domain", M.ParseSocksaddrHostPort("www.Google.com.", 443), nil, "google.com"},
		{"multi-label suffix", M.ParseSocksaddrHostPort("a.b.example.co.uk", 443), nil, "example.co.uk"},
		{"single label", M.ParseSocksaddrHostPort("localhost", 80), nil, "localhost"},
		{"address only", address, nil, "203.0.113.7"},
		{"mapped address", M.ParseSocksaddr("[::ffff:203.0.113.7]:443"), nil, "203.0.113.7"},
		{"sniffed domain", address, &adapter.InboundContext{Destination: address, Domain: "rr1.video.youtube.com"}, "youtube.com"},
		{"resolved domain", address, &adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("api.github.com", 443), DestinationAddresses: []netip.Addr{address.Addr}}, "github.com"},
		{"nested dial", M.ParseSocksaddr("192.0.2.53:53"), &adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("example.com", 443), Domain: "example.com"}, "192.0.2.53"},
	} {
		ctx := context.Background()
		if item.metadata != nil {
			ctx = adapter.WithContext(ctx, item.metadata)
		}
		if got := siteKey(ctx, item.destination); got != item.want {
			t.Errorf("%s: got %q, want %q", item.name, got, item.want)
		}
	}
}

func testMembers(tags ...string) []*member {
	members := make([]*member, len(tags))
	for i, tag := range tags {
		members[i] = &member{index: i, tag: tag, hash: mix(stringHash(tag))}
	}
	return members
}

func topChoices(members []*member, keys []string) []string {
	result := make([]string, len(keys))
	for i, key := range keys {
		result[i] = rank(members, key)[0].tag
	}
	return result
}

func TestRankIsStableBalancedAndMinimal(t *testing.T) {
	keys := make([]string, 30000)
	for i := range keys {
		keys[i] = fmt.Sprintf("site-%d.example", i)
	}
	three := topChoices(testMembers("jp-1", "jp-2", "jp-3"), keys)
	if again := topChoices(testMembers("jp-1", "jp-2", "jp-3"), keys); !slices.Equal(three, again) {
		t.Fatal("ranking is not deterministic")
	}
	if reordered := topChoices(testMembers("jp-3", "jp-1", "jp-2"), keys); !slices.Equal(three, reordered) {
		t.Fatal("ranking depends on member order")
	}
	counts := map[string]int{}
	for _, tag := range three {
		counts[tag]++
	}
	for tag, count := range counts {
		if share := float64(count) / float64(len(keys)); share < 0.31 || share > 0.36 {
			t.Errorf("%s takes %.3f of sites", tag, share)
		}
	}

	// Removing a member moves only the sites it had.
	two := topChoices(testMembers("jp-1", "jp-2"), keys)
	for i := range keys {
		if three[i] != "jp-3" && two[i] != three[i] {
			t.Fatalf("site %s moved from %s to %s", keys[i], three[i], two[i])
		}
	}
	// Adding a member moves about a quarter of the sites, all to it.
	four := topChoices(testMembers("jp-1", "jp-2", "jp-3", "jp-4"), keys)
	moved := 0
	for i := range keys {
		if four[i] != three[i] {
			moved++
			if four[i] != "jp-4" {
				t.Fatalf("site %s moved between existing members", keys[i])
			}
		}
	}
	if share := float64(moved) / float64(len(keys)); share < 0.22 || share > 0.28 {
		t.Errorf("adding a member moved %.3f of sites", share)
	}
}
