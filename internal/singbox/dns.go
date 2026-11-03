package singbox

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/HyperPteLtd/libbox/internal/contract"
	mDNS "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// Host local DNS uses the supplied physical-network DNS servers and the core's
// protected dialer. It never silently falls back to the process/VPN resolver.
func registerHostDNS(ctx context.Context, p *platform) {
	registry := service.FromContext[adapter.DNSTransportRegistry](ctx).(*dns.TransportRegistry)
	dns.RegisterTransport[option.LocalDNSServerOptions](registry, C.DNSTypeLocal, func(ctx context.Context, logger log.ContextLogger, tag string, options option.LocalDNSServerOptions) (adapter.DNSTransport, error) {
		dialer, err := dns.NewLocalDialer(ctx, options)
		if err != nil {
			return nil, err
		}
		preferred, err := local.NewPreferredDomainResolver(ctx, logger, options)
		if err != nil {
			return nil, err
		}
		return &hostDNS{TransportAdapter: dns.NewTransportAdapterWithLocalOptions(C.DNSTypeLocal, tag, options), platform: p, dialer: dialer, preferred: preferred}, nil
	})
}

type hostDNS struct {
	dns.TransportAdapter
	platform  *platform
	dialer    N.Dialer
	preferred *local.PreferredDomainResolver
}

var _ adapter.DNSTransport = (*hostDNS)(nil)

func (h *hostDNS) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	h.preferred.Start(stage)
	return nil
}
func (*hostDNS) Reset()                               {}
func (h *hostDNS) PreferredDomain(domain string) bool { return h.preferred.PreferredDomain(domain) }
func (h *hostDNS) ServerAddresses() []netip.Addr {
	result := []netip.Addr{}
	if i := h.platform.defaultInterface(); i != nil {
		for _, raw := range i.DNSServers {
			addr, err := netip.ParseAddr(raw)
			if err == nil {
				result = append(result, addr)
			}
		}
	}
	return result
}
func (h *hostDNS) SearchDomains() []string {
	if i := h.platform.defaultInterface(); i != nil {
		return i.DNSSearchDomains
	}
	return nil
}
func (h *hostDNS) Environment() []string {
	result := []string{}
	for _, addr := range h.ServerAddresses() {
		result = append(result, addr.String())
	}
	return append(result, h.SearchDomains()...)
}
func (h *hostDNS) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	if message == nil || len(message.Question) == 0 {
		return nil, contract.Fail("invalid_argument", "DNS request has no question")
	}
	if response := h.preferred.Lookup(message); response != nil {
		return response, nil
	}
	servers := h.ServerAddresses()
	if len(servers) == 0 {
		return nil, contract.Fail("not_found", "host network has no DNS servers")
	}
	var failures []error
	for _, server := range servers {
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		response, err := h.exchange(queryCtx, message, server, "udp")
		if err == nil && response.Truncated {
			response, err = h.exchange(queryCtx, message, server, "tcp")
		}
		cancel()
		if err == nil {
			return response, nil
		}
		failures = append(failures, err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.Join(failures...)
}
func (h *hostDNS) exchange(ctx context.Context, message *mDNS.Msg, address netip.Addr, network string) (*mDNS.Msg, error) {
	conn, err := h.dialer.DialContext(ctx, network, M.SocksaddrFrom(address, 53))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	client := &mDNS.Client{Net: network}
	response, _, err := client.ExchangeWithConnContext(ctx, message, &mDNS.Conn{Conn: conn})
	return response, err
}
func (h *hostDNS) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	go func() { callback(h.Exchange(ctx, message)) }()
}
