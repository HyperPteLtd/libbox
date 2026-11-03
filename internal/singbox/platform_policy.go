package singbox

import (
	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
)

// Apple VPNs share host-owned raw utun descriptors. Proxy-only native mode is
// still valid; Windows/Linux can continue to let the core own their TUN.
func preparePlatformOptions(parsed *option.Options, options contract.Options, hooks contract.Hooks, validation bool, goos string) error {
	apple := goos == "darwin" || goos == "ios"
	tunCount := 0
	for _, inbound := range parsed.Inbounds {
		tunOptions, ok := inbound.Options.(*option.TunInboundOptions)
		if !ok {
			continue
		}
		tunCount++
		switch tunOptions.Stack {
		case "", "system":
		case "mixed", "gvisor":
			if !tun.WithGVisor {
				return contract.Fail("unsupported", "this TUN stack requires a with_gvisor build")
			}
		default:
			return contract.Fail("unsupported", "sing-box v1.14.4 supports system, mixed, and gvisor TUN stacks")
		}
		if apple && options.NetworkMode != "host" {
			return contract.Fail("unsupported", "Apple TUN uses host-owned raw utun FD; set network_mode=HOST")
		}
		if options.NetworkMode != "host" {
			continue
		}
		if goos == "windows" {
			return contract.Fail("unsupported", "Windows TUN requires network_mode=NATIVE; external FD/HANDLE attachment is unsupported")
		}
		if options.UnderNetworkExtension && options.IncludeAllNetworks {
			if !tun.WithGVisor || tunOptions.Stack == "system" || tunOptions.Stack == "mixed" {
				return contract.Fail("unsupported", "include_all_networks requires the gvisor stack and a with_gvisor build")
			}
		}
		if !validation && hooks.OpenTun == nil {
			return contract.Fail("unsupported", "host TUN requires OPEN_TUN capability")
		}
		if tunOptions.AutoRedirect {
			return contract.Fail("unsupported", "auto_redirect is not supported in host mode")
		}
	}
	if apple && options.UnderNetworkExtension && tunCount != 1 {
		return contract.Fail("unsupported", "a NetworkExtension provider requires exactly one TUN interface")
	}
	if options.NetworkMode == "host" {
		if parsed.Route == nil {
			parsed.Route = &option.RouteOptions{}
		}
		// iOS NECP routes the provider's own sockets. Do not pin them to a cached
		// cellular APN. macOS still binds to the physical path to avoid a TUN loop.
		systemManaged := goos == "ios" && options.UnderNetworkExtension
		if systemManaged && parsed.Route.DefaultNetworkStrategy != nil {
			return contract.Fail("unsupported", "iOS NetworkExtension uses system-managed egress, not default_network_strategy")
		}
		parsed.Route.AutoDetectInterface = !systemManaged
	}
	return nil
}
