package libbox

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/HyperPteLtd/libbox/internal/control"
	"github.com/HyperPteLtd/libbox/internal/singbox"
	"github.com/HyperPteLtd/libbox/internal/syncutil"
	pb "github.com/HyperPteLtd/libbox/protocol/v1"
	"google.golang.org/protobuf/proto"
)

// HostMessageHandler is the only reverse-call interface for new hosts. Both
// buffers contain versioned protobuf messages. It runs in the embedding process;
// file descriptors cannot be sent to another process as integers. Implementations
// must respect the supplied time budget and must not reenter lifecycle methods.
type HostMessageHandler interface {
	HandleHostMessage(request []byte) ([]byte, error)
}

// MessageSession is a protobuf-only control endpoint. Native construction merely
// allocates an endpoint; create_engine/destroy_engine messages own the engine.
// The zero value is invalid. Close is transport cleanup, not a second wire API.
type MessageSession struct {
	op           syncutil.Mutex
	mu           sync.RWMutex
	initialized  bool
	closed       bool
	closing      bool
	engine       *engine
	host         HostMessageHandler
	hostSequence atomic.Uint64
}

func NewMessageSession(handler HostMessageHandler) *MessageSession {
	return &MessageSession{initialized: true, host: handler}
}
func (s *MessageSession) HandleMessage(request []byte) []byte {
	if s == nil || !s.initialized {
		return control.FailureFor(request, contract.Fail("uninitialized", "create a session with NewMessageSession"))
	}
	s.mu.RLock()
	closed := s.closed || s.closing
	s.mu.RUnlock()
	if closed {
		return control.FailureFor(request, contract.Fail("closed", "message session is closed"))
	}
	return control.Handle(context.Background(), sessionControl{s}, request)
}
func (s *MessageSession) Close() error {
	if s == nil || !s.initialized {
		return contract.Fail("uninitialized", "message session is uninitialized")
	}
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	e := s.engine
	s.engine = nil
	s.mu.Unlock()
	var err error
	if e != nil {
		err = e.Close()
	}
	s.mu.Lock()
	s.host = nil
	s.mu.Unlock()
	return err
}

type sessionControl struct{ s *MessageSession }

func (c sessionControl) lookup(ctx context.Context) (engineControl, error) {
	if err := ctx.Err(); err != nil {
		return engineControl{}, err
	}
	c.s.mu.RLock()
	e, closed := c.s.engine, c.s.closed || c.s.closing
	c.s.mu.RUnlock()
	if closed {
		return engineControl{}, contract.Fail("closed", "message session is closed")
	}
	if e == nil {
		return engineControl{}, contract.Fail("uninitialized", "send create_engine first")
	}
	return engineControl{e}, nil
}
func (c sessionControl) Create(ctx context.Context, request *pb.EngineOptions) (contract.Info, error) {
	if err := c.s.op.LockContext(ctx); err != nil {
		return contract.Info{}, err
	}
	defer c.s.op.Unlock()
	c.s.mu.RLock()
	exists, closed := c.s.engine != nil, c.s.closed || c.s.closing
	c.s.mu.RUnlock()
	if closed {
		return contract.Info{}, contract.Fail("closed", "message session is closed")
	}
	if exists {
		return contract.Info{}, contract.Fail("invalid_state", "engine already exists")
	}
	if request == nil {
		return contract.Info{}, contract.Fail("invalid_argument", "missing engine options")
	}
	options := contract.Options{WorkingDirectory: request.WorkingDirectory, TempDirectory: request.TempDirectory, EventBuffer: 256, UnderNetworkExtension: request.UnderNetworkExtension, IncludeAllNetworks: request.IncludeAllNetworks, MemoryLimitBytes: request.MemoryLimitBytes, GCPercent: int(request.GcPercent)}
	switch request.NetworkMode {
	case pb.EngineOptions_NATIVE:
		options.NetworkMode = "native"
	case pb.EngineOptions_HOST:
		options.NetworkMode = "host"
	default:
		return contract.Info{}, contract.Fail("invalid_argument", "unknown network mode")
	}
	switch request.MemoryProfile {
	case pb.EngineOptions_AUTO:
		options.MemoryProfile = "auto"
	case pb.EngineOptions_DEFAULT:
		options.MemoryProfile = "default"
	case pb.EngineOptions_NETWORK_EXTENSION:
		options.MemoryProfile = "network_extension"
	default:
		return contract.Info{}, contract.Fail("invalid_argument", "unknown memory profile")
	}
	if request.EventBuffer != nil {
		options.EventBuffer = int(*request.EventBuffer)
	}
	if request.GcPercent > 1000 {
		return contract.Info{}, contract.Fail("invalid_argument", "gc_percent exceeds 1000")
	}
	hooks := contract.Hooks{}
	if len(request.HostCapabilities) > 0 && (options.NetworkMode != "host" || c.s.host == nil) {
		return contract.Info{}, contract.Fail("invalid_argument", "host capabilities require host mode and a handler")
	}
	for _, capability := range request.HostCapabilities {
		switch capability {
		case pb.EngineOptions_OPEN_TUN:
			hooks.OpenTun = c.s.openTun
		case pb.EngineOptions_PROTECT_SOCKET:
			hooks.ProtectSocket = c.s.protectSocket
		case pb.EngineOptions_CONNECTION_OWNER:
			hooks.FindConnectionOwner = c.s.findOwner
		default:
			return contract.Info{}, contract.Fail("unsupported", "unknown host capability")
		}
	}
	e, err := newEngineOptions(options, singbox.Factory{})
	if err != nil {
		return contract.Info{}, err
	}
	e.hooks = hooks
	if err = ctx.Err(); err != nil {
		_ = e.Close()
		return contract.Info{}, err
	}
	c.s.mu.Lock()
	if c.s.closing || c.s.closed {
		c.s.mu.Unlock()
		_ = e.Close()
		return contract.Info{}, contract.Fail("closed", "message session is closing")
	}
	c.s.engine = e
	c.s.mu.Unlock()
	return singbox.BuildInfo(), nil
}
func (c sessionControl) Destroy(ctx context.Context) error {
	if err := c.s.op.LockContext(ctx); err != nil {
		return err
	}
	defer c.s.op.Unlock()
	c.s.mu.Lock()
	e := c.s.engine
	c.s.engine = nil
	c.s.mu.Unlock()
	if e != nil {
		return e.Close()
	}
	return nil
}
func (c sessionControl) Info(ctx context.Context) (contract.Info, error) {
	if err := ctx.Err(); err != nil {
		return contract.Info{}, err
	}
	return singbox.BuildInfo(), nil
}
func (c sessionControl) CheckConfig(ctx context.Context, value string) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.CheckConfig(ctx, value)
}
func (c sessionControl) Start(ctx context.Context, value string) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.Start(ctx, value)
}
func (c sessionControl) Reload(ctx context.Context, value string) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.Reload(ctx, value)
}
func (c sessionControl) Stop(ctx context.Context) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.Stop(ctx)
}
func (c sessionControl) Status(ctx context.Context) (contract.Status, error) {
	e, err := c.lookup(ctx)
	if err != nil {
		return contract.Status{}, err
	}
	return e.Status(ctx)
}
func (c sessionControl) Groups(ctx context.Context) ([]contract.Group, error) {
	e, err := c.lookup(ctx)
	if err != nil {
		return nil, err
	}
	return e.Groups(ctx)
}
func (c sessionControl) Outbounds(ctx context.Context) ([]contract.Outbound, error) {
	e, err := c.lookup(ctx)
	if err != nil {
		return nil, err
	}
	return e.Outbounds(ctx)
}
func (c sessionControl) SelectOutbound(ctx context.Context, group, outbound string) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.SelectOutbound(ctx, group, outbound)
}
func (c sessionControl) URLTest(ctx context.Context, outbound, url string, timeout int32) (int32, error) {
	e, err := c.lookup(ctx)
	if err != nil {
		return 0, err
	}
	return e.URLTest(ctx, outbound, url, timeout)
}
func (c sessionControl) SetMode(ctx context.Context, value string) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.SetMode(ctx, value)
}
func (c sessionControl) SetPaused(ctx context.Context, value bool) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.SetPaused(ctx, value)
}
func (c sessionControl) CloseConnections(ctx context.Context) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.CloseConnections(ctx)
}
func (c sessionControl) NextEvent(ctx context.Context, timeout int32) (*contract.Event, error) {
	e, err := c.lookup(ctx)
	if err != nil {
		return nil, err
	}
	return e.NextEvent(ctx, timeout)
}
func (c sessionControl) UpdateNetwork(ctx context.Context, value contract.Network) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.UpdateNetwork(ctx, value)
}
func (c sessionControl) MemoryPressure(ctx context.Context, level int32) error {
	e, err := c.lookup(ctx)
	if err != nil {
		return err
	}
	return e.MemoryPressure(ctx, level)
}

func (s *MessageSession) callHost(request *pb.HostRequest) (*pb.HostResponse, error) {
	s.mu.RLock()
	handler := s.host
	s.mu.RUnlock()
	if handler == nil {
		return nil, contract.Fail("closed", "host message handler is closed")
	}
	request.ProtocolVersion = 1
	request.Id = s.hostSequence.Add(1)
	data, err := proto.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(data) > control.MaxMessageBytes {
		return nil, contract.Fail("host_error", "host request exceeds limit")
	}
	encoded, err := handler.HandleHostMessage(data)
	if err != nil {
		return nil, contract.Wrap("host_error", err)
	}
	if len(encoded) > control.MaxHostResponseBytes {
		return nil, contract.Fail("host_error", "host response exceeds limit")
	}
	var response pb.HostResponse
	if err = (proto.UnmarshalOptions{RecursionLimit: 64}).Unmarshal(encoded, &response); err != nil {
		return nil, contract.Fail("host_error", "invalid host protobuf response")
	}
	if response.ProtocolVersion != 1 || response.Id != request.Id {
		return nil, contract.Fail("host_error", "host response version/id mismatch")
	}
	if failure := response.GetError(); failure != nil {
		return nil, contract.Fail("host_error", failure.Code+": "+failure.Message)
	}
	return &response, nil
}
func (s *MessageSession) openTun(options contract.TunOptions) (int32, error) {
	value := &pb.TunOptions{Name: options.Name, Mtu: options.MTU, Addresses: options.Addresses, DnsServers: options.DNSServers, DnsMode: options.DNSMode, AutoRoute: options.AutoRoute, StrictRoute: options.StrictRoute, Routes: options.Routes, ExcludeRoutes: options.ExcludeRoutes, IncludePackages: options.IncludePackages, ExcludePackages: options.ExcludePackages}
	if proxy := options.HTTPProxy; proxy != nil {
		value.HttpProxy = &pb.HTTPProxy{Enabled: proxy.Enabled, Server: proxy.Server, Port: uint32(proxy.Port), BypassDomains: proxy.BypassDomains, MatchDomains: proxy.MatchDomains}
	}
	response, err := s.callHost(&pb.HostRequest{TimeoutMs: 25000, Method: &pb.HostRequest_OpenTun{OpenTun: value}})
	if err != nil {
		return -1, err
	}
	if result := response.GetTun(); result != nil && result.Fd >= 0 {
		return result.Fd, nil
	}
	return -1, contract.Fail("host_error", "host did not return a valid TUN descriptor")
}
func (s *MessageSession) protectSocket(fd int32) error {
	response, err := s.callHost(&pb.HostRequest{TimeoutMs: 5000, Method: &pb.HostRequest_ProtectSocket{ProtectSocket: &pb.Socket{Fd: fd}}})
	if err != nil {
		return err
	}
	if response.GetEmpty() == nil {
		return contract.Fail("host_error", "host did not acknowledge socket protection")
	}
	return nil
}
func (s *MessageSession) findOwner(request contract.ConnectionRequest) (contract.ConnectionOwner, error) {
	response, err := s.callHost(&pb.HostRequest{TimeoutMs: 5000, Method: &pb.HostRequest_FindConnectionOwner{FindConnectionOwner: &pb.ConnectionRequest{Protocol: request.Protocol, SourceAddress: request.SourceAddress, SourcePort: request.SourcePort, DestinationAddress: request.DestinationAddress, DestinationPort: request.DestinationPort}}})
	if err != nil {
		return contract.ConnectionOwner{}, err
	}
	owner := response.GetConnectionOwner()
	if owner == nil {
		return contract.ConnectionOwner{}, contract.Fail("host_error", "missing connection owner")
	}
	return contract.ConnectionOwner{UserID: owner.UserId, ProcessID: owner.ProcessId, UserName: owner.UserName, ProcessPaths: owner.ProcessPaths, PackageNames: owner.PackageNames}, nil
}
