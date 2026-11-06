package loadbalance

import (
	"cmp"
	"context"
	"hash/fnv"
	"net/netip"
	"slices"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/publicsuffix"
)

// siteKey names the site a connection belongs to: the registrable domain
// (eTLD+1) when one is known, otherwise the destination IP. Router metadata
// carries the domain when the destination was resolved or sniffed first.
func siteKey(ctx context.Context, destination M.Socksaddr) string {
	if destination.IsDomain() {
		return registrableDomain(destination.Fqdn)
	}
	if metadata := adapter.ContextFrom(ctx); metadata != nil && sameTarget(metadata, destination) {
		if metadata.Destination.IsDomain() {
			return registrableDomain(metadata.Destination.Fqdn)
		}
		if metadata.Domain != "" {
			return registrableDomain(metadata.Domain)
		}
	}
	return destination.Addr.Unmap().String()
}

// Metadata stays in the context for nested dials, such as a DNS server or a
// chained proxy that uses this group as its detour. Trust it only when it
// describes the address being dialed.
func sameTarget(metadata *adapter.InboundContext, destination M.Socksaddr) bool {
	address := destination.Addr.Unmap()
	if metadata.Destination.IsIP() && metadata.Destination.Addr.Unmap() == address {
		return true
	}
	return slices.ContainsFunc(metadata.DestinationAddresses, func(it netip.Addr) bool { return it.Unmap() == address })
}

func registrableDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	if site, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil {
		return site
	}
	return domain
}

func stringHash(value string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

// mix is the MurmurHash3 64-bit finalizer. FNV alone diffuses the last input
// bytes poorly, and rendezvous scores need every output bit to depend on both
// the site and the member.
func mix(x uint64) uint64 {
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

// rank orders members for a site by rendezvous (highest random weight)
// hashing. Every site gets its own fixed preference order over all configured
// members, and health only filters that order: a failed member hands its
// sites to their next preference, and a new member takes about 1/n of the
// sites while every other site keeps its member.
func rank(members []*member, key string) []*member {
	keyHash := stringHash(key)
	ranked := slices.Clone(members)
	slices.SortStableFunc(ranked, func(a, b *member) int {
		return cmp.Compare(mix(keyHash^b.hash), mix(keyHash^a.hash))
	})
	return ranked
}
