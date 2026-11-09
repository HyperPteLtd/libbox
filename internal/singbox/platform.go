package singbox

import (
	"context"
	"net"
	"net/netip"
	"sync"

	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
)

type platform struct {
	mu              sync.RWMutex
	callbackMu      sync.RWMutex
	callbacksClosed bool
	options         contract.Options
	hooks           contract.Hooks
	network         contract.Network
	manager         adapter.NetworkManager
	monitorStarted  bool
	monitor         *monitor
	addresses       []netip.Addr
}

var _ adapter.PlatformInterface = (*platform)(nil)

func newPlatform(options contract.Options, hooks contract.Hooks, network contract.Network) *platform {
	p := &platform{options: options, hooks: hooks, network: network}
	p.monitor = &monitor{platform: p}
	return p
}
func (p *platform) Initialize(manager adapter.NetworkManager) error { p.manager = manager; return nil }
func (p *platform) UsePlatformAutoDetectInterfaceControl() bool     { return p.hooks.ProtectSocket != nil }
func (p *platform) AutoDetectInterfaceControl(fd int) error {
	p.callbackMu.RLock()
	defer p.callbackMu.RUnlock()
	if p.callbacksClosed {
		return contract.Fail("closed", "host callbacks are closed")
	}
	if p.hooks.ProtectSocket == nil {
		return unsupported()
	}
	return p.hooks.ProtectSocket(int32(fd))
}

// Wait for callbacks already executing and reject late core calls. This is the
// lifetime barrier for C callback context pointers and managed host delegates.
func (p *platform) closeCallbacks() {
	p.callbackMu.Lock()
	p.callbacksClosed = true
	p.callbackMu.Unlock()
}
func (p *platform) UsePlatformInterface() bool               { return true }
func (p *platform) UsePlatformDefaultInterfaceMonitor() bool { return true }
func (p *platform) CreateDefaultInterfaceMonitor(logger.Logger) tun.DefaultInterfaceMonitor {
	return p.monitor
}
func (p *platform) UsePlatformNetworkInterfaces() bool       { return true }
func (p *platform) UnderNetworkExtension() bool              { return p.options.UnderNetworkExtension }
func (p *platform) NetworkExtensionIncludeAllNetworks() bool { return p.options.IncludeAllNetworks }
func (p *platform) MyInterfaceAddress() []netip.Addr {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]netip.Addr(nil), p.addresses...)
}
func (p *platform) ClearDNSCache()                       {}
func (p *platform) RequestPermissionForWIFIState() error { return nil }
func (p *platform) UsePlatformWIFIMonitor() bool         { return true }
func (p *platform) ReadWIFIState(context.Context) adapter.WIFIState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return adapter.WIFIState{SSID: p.network.WIFISSID, BSSID: p.network.WIFIBSSID}
}
func (p *platform) UsePlatformConnectionOwnerFinder() bool { return p.hooks.FindConnectionOwner != nil }
func (p *platform) FindConnectionOwner(request *adapter.FindConnectionOwnerRequest) (*adapter.ConnectionOwner, error) {
	p.callbackMu.RLock()
	defer p.callbackMu.RUnlock()
	if p.callbacksClosed {
		return nil, contract.Fail("closed", "host callbacks are closed")
	}
	if p.hooks.FindConnectionOwner == nil {
		return nil, unsupported()
	}
	owner, err := p.hooks.FindConnectionOwner(contract.ConnectionRequest{APIVersion: contract.APIVersion, Protocol: request.IpProtocol, SourceAddress: request.SourceAddress, SourcePort: request.SourcePort, DestinationAddress: request.DestinationAddress, DestinationPort: request.DestinationPort})
	if err != nil {
		return nil, err
	}
	return &adapter.ConnectionOwner{UserId: owner.UserID, ProcessID: owner.ProcessID, UserName: owner.UserName, ProcessPaths: append([]string(nil), owner.ProcessPaths...), PackageNames: append([]string(nil), owner.PackageNames...)}, nil
}

func interfaceValue(i contract.Interface) adapter.NetworkInterface {
	addresses := make([]netip.Prefix, 0, len(i.Addresses))
	for _, address := range i.Addresses {
		prefix, _ := netip.ParsePrefix(address)
		addresses = append(addresses, prefix)
	}
	flags := net.FlagUp | net.FlagRunning | net.FlagMulticast
	if i.Loopback {
		flags |= net.FlagLoopback
	}
	typeID := C.InterfaceTypeOther
	switch i.Type {
	case "wifi":
		typeID = C.InterfaceTypeWIFI
	case "cellular":
		typeID = C.InterfaceTypeCellular
	case "ethernet":
		typeID = C.InterfaceTypeEthernet
	}
	return adapter.NetworkInterface{Interface: control.Interface{Index: i.Index, Name: i.Name, MTU: i.MTU, Addresses: addresses, Flags: flags}, Type: typeID, DNSServers: append([]string(nil), i.DNSServers...), DNSSearchDomains: append([]string(nil), i.DNSSearchDomains...), Expensive: i.Metered, Constrained: i.Constrained}
}
func (p *platform) NetworkInterfaces() ([]adapter.NetworkInterface, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]adapter.NetworkInterface, 0, len(p.network.Interfaces))
	for _, i := range p.network.Interfaces {
		result = append(result, interfaceValue(i))
	}
	return result, nil
}
func (p *platform) defaultInterface() *adapter.NetworkInterface {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, i := range p.network.Interfaces {
		if i.Index == p.network.DefaultInterface {
			value := interfaceValue(i)
			return &value
		}
	}
	return nil
}
func (p *platform) defaultSearchDomains() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, i := range p.network.Interfaces {
		if i.Index == p.network.DefaultInterface {
			return append([]string(nil), i.DNSSearchDomains...)
		}
	}
	return nil
}
func (p *platform) update(network contract.Network) error {
	p.mu.Lock()
	p.network = network
	started := p.monitorStarted
	p.mu.Unlock()
	if !started {
		return nil
	}
	if p.manager != nil {
		if err := p.manager.UpdateInterfaces(); err != nil {
			return err
		}
	}
	p.monitor.notify()
	return nil
}
func (p *platform) networkReady() bool { p.mu.RLock(); defer p.mu.RUnlock(); return p.monitorStarted }

func (p *platform) OpenInterface(options *tun.Options, platformOptions option.TunPlatformOptions) (tun.Tun, error) {
	p.callbackMu.RLock()
	defer p.callbackMu.RUnlock()
	if p.callbacksClosed {
		return nil, contract.Fail("closed", "host callbacks are closed")
	}
	if p.hooks.OpenTun == nil {
		return nil, contract.Fail("unsupported", "host did not supply a TUN opener")
	}
	if len(options.IncludeUID)+len(options.ExcludeUID)+len(options.IncludeAndroidUser)+len(options.IncludeMACAddress)+len(options.ExcludeMACAddress) > 0 {
		return nil, contract.Fail("unsupported", "host TUN supports package filters, not UID/user/MAC filters")
	}
	routes, err := options.BuildAutoRouteRanges(true)
	if err != nil {
		return nil, err
	}
	servers, err := options.DNSServerAddress()
	if err != nil {
		return nil, err
	}
	request := contract.TunOptions{APIVersion: contract.APIVersion, Name: options.Name, MTU: options.MTU, Addresses: []string{}, Routes: []string{}, ExcludeRoutes: []string{}, DNSServers: []string{}, DNSMode: options.DNSModeOrDefault(), AutoRoute: options.AutoRoute, StrictRoute: options.StrictRoute, IncludePackages: append([]string{}, options.IncludePackage...), ExcludePackages: append([]string{}, options.ExcludePackage...)}
	for _, prefix := range append(append([]netip.Prefix{}, options.Inet4Address...), options.Inet6Address...) {
		request.Addresses = append(request.Addresses, prefix.String())
	}
	for _, prefix := range routes {
		request.Routes = append(request.Routes, prefix.String())
	}
	for _, prefix := range append(append([]netip.Prefix{}, options.Inet4RouteExcludeAddress...), options.Inet6RouteExcludeAddress...) {
		request.ExcludeRoutes = append(request.ExcludeRoutes, prefix.String())
	}
	for _, server := range servers {
		request.DNSServers = append(request.DNSServers, server.String())
	}
	if proxy := platformOptions.HTTPProxy; proxy != nil {
		request.HTTPProxy = &contract.HTTPProxy{Enabled: proxy.Enabled, Server: proxy.Server, Port: proxy.ServerPort, BypassDomains: append([]string{}, proxy.BypassDomain...), MatchDomains: append([]string{}, proxy.MatchDomain...)}
	}
	fd, err := p.hooks.OpenTun(request)
	if err != nil {
		return nil, err
	}
	if fd < 0 {
		return nil, contract.Fail("invalid_argument", "host returned a negative TUN descriptor")
	}
	owned, name, err := duplicateTun(int(fd))
	if err != nil {
		return nil, err
	}
	copyOptions := *options
	copyOptions.FileDescriptor, copyOptions.Name = owned, name
	copyOptions.EXP_ExternalConfiguration = true
	device, err := tun.New(copyOptions)
	if err != nil {
		closeDescriptor(owned)
		return nil, err
	}
	options.Name = name
	p.monitor.RegisterMyInterface(name)
	p.mu.Lock()
	for _, prefix := range append(append([]netip.Prefix{}, options.Inet4Address...), options.Inet6Address...) {
		p.addresses = append(p.addresses, prefix.Addr())
	}
	p.mu.Unlock()
	return device, nil
}
func (p *platform) ProcessPlatformOptions(option.TunPlatformOptions) error { return nil }

// Unsupported optional upstream features stay internal; adding one upstream
// method does not expand any public host interface.
func unsupported() error {
	return contract.Fail("unsupported", "platform capability is not implemented")
}
func (*platform) UsePlatformNotification() bool                             { return false }
func (*platform) SendNotification(*adapter.Notification) error              { return unsupported() }
func (*platform) CancelNotification(string, int32) error                    { return nil }
func (*platform) UsePlatformNeighborResolver() bool                         { return false }
func (*platform) StartNeighborMonitor(adapter.NeighborUpdateListener) error { return unsupported() }
func (*platform) CloseNeighborMonitor(adapter.NeighborUpdateListener) error { return nil }
func (*platform) UsePlatformShell() bool                                    { return false }
func (*platform) CheckPlatformShell() error                                 { return unsupported() }
func (*platform) OpenShellSession(*adapter.PlatformUser, string, []string, string, int32, int32) (adapter.ShellSession, error) {
	return nil, unsupported()
}
func (*platform) LookupUser(string) (*adapter.PlatformUser, error) { return nil, unsupported() }
func (*platform) LookupSFTPServer() (string, error)                { return "", unsupported() }
func (*platform) ReadSystemSSHHostKey() ([]byte, error)            { return nil, unsupported() }
func (*platform) TailscaleHostname() string                        { return "" }
func (*platform) UsePlatformBridge() bool                          { return false }
func (*platform) CreateBridge(adapter.BridgeOptions) (adapter.BridgeSession, error) {
	return nil, unsupported()
}
func (*platform) UsePlatformAutoRedirect() bool { return false }
func (*platform) CreateAutoRedirect(adapter.AutoRedirectOptions) (adapter.AutoRedirectSession, error) {
	return nil, unsupported()
}
