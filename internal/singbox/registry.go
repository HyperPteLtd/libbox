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

// CoreContext is coreContext for the command-line build (cmd/sing-box), so
// configurations run by the CLI accept the same outbound types as the SDK.
func CoreContext(ctx context.Context) context.Context {
	return coreContext(ctx)
}
