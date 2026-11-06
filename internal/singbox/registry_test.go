package singbox

import (
	"context"
	"slices"
	"testing"

	"github.com/HyperPteLtd/libbox/internal/singbox/loadbalance"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

func registryTypes(ctx context.Context) map[string][]string {
	return map[string][]string{
		"inbound":     service.FromContext[option.InboundOptionsRegistry](ctx).OptionTypes(),
		"outbound":    service.FromContext[option.OutboundOptionsRegistry](ctx).OptionTypes(),
		"endpoint":    service.FromContext[option.EndpointOptionsRegistry](ctx).OptionTypes(),
		"dns":         service.FromContext[option.DNSTransportOptionsRegistry](ctx).OptionTypes(),
		"service":     service.FromContext[option.ServiceOptionsRegistry](ctx).OptionTypes(),
		"certificate": service.FromContext[option.CertificateProviderOptionsRegistry](ctx).OptionTypes(),
	}
}

// The SDK context must match the upstream default, including the protocols
// that build tags add, except for the SDK-owned outbound types.
func TestCoreContextExtendsUpstreamRegistries(t *testing.T) {
	upstream := registryTypes(include.Context(context.Background()))
	sdk := registryTypes(coreContext(context.Background()))
	if slices.Contains(upstream["outbound"], loadbalance.Type) {
		t.Fatal("upstream now registers " + loadbalance.Type + "; review the SDK type before upgrading")
	}
	upstream["outbound"] = append(upstream["outbound"], loadbalance.Type)
	slices.Sort(upstream["outbound"])
	for kind, types := range upstream {
		if !slices.Equal(sdk[kind], types) {
			t.Errorf("%s registry differs:\nupstream+sdk %v\nsdk          %v", kind, types, sdk[kind])
		}
	}
}
