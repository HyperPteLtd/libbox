package singbox

import (
	"context"

	"github.com/HyperPteLtd/libbox/internal/singbox/loadbalance"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
)

// coreContext installs the same registries as include.Context, whose build
// tags still select the optional upstream protocols, and adds the outbound
// types owned by the SDK.
func coreContext(ctx context.Context) context.Context {
	outbounds := include.OutboundRegistry()
	loadbalance.Register(outbounds)
	return box.Context(ctx, include.InboundRegistry(), outbounds, include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
}
