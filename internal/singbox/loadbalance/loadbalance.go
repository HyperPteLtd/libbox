// Package loadbalance adds the SDK-owned "loadbalance" outbound group to the
// pinned sing-box core without replacing any upstream module. A group spreads
// connections over its members by destination site, keeps sites on the same
// member while it stays healthy, and switches to a fallback outbound when no
// member is healthy.
package loadbalance

import (
	"context"
	"io"
	"net"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

const (
	Type                      = "loadbalance"
	StrategyConsistentHashing = "consistent-hashing"
	defaultRecoveryChecks     = 2
	// Retries stop once a connection exists, so they never replay data. The
	// cap bounds how long one dial can spend on members that time out.
	maxAttempts = 3
)

type Options struct {
	Outbounds                 []string           `json:"outbounds"`
	Strategy                  string             `json:"strategy,omitempty"`
	URL                       string             `json:"url,omitempty"`
	Interval                  badoption.Duration `json:"interval,omitempty"`
	Tolerance                 uint16             `json:"tolerance,omitempty"`
	IdleTimeout               badoption.Duration `json:"idle_timeout,omitempty"`
	RecoveryChecks            uint16             `json:"recovery_checks,omitempty"`
	Fallback                  string             `json:"fallback,omitempty"`
	InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
}

// Health describes a group after a member changed health. Healthy members take
// new connections; unhealthy ones include members still in recovery. Fallback
// is set while new connections use the fallback outbound.
type Health struct {
	Group     string
	Healthy   []string
	Unhealthy []string
	Fallback  string
}

// Observer receives every health change in order. It runs while the group
// holds its state lock, so it must not block or call back into the group.
type Observer func(Health)

type observerKey struct{}

func ContextWithObserver(ctx context.Context, observer Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

func Register(registry *outbound.Registry) {
	outbound.Register[Options](registry, Type, New)
}

var (
	_ adapter.OutboundGroup           = (*Group)(nil)
	_ adapter.URLTestGroup            = (*Group)(nil)
	_ adapter.InterfaceUpdateListener = (*Group)(nil)
	_ adapter.Referrer                = (*Group)(nil)
	_ adapter.Lifecycle               = (*Group)(nil)
)

type Group struct {
	outbound.Adapter
	ctx               context.Context
	logger            log.ContextLogger
	manager           adapter.OutboundManager
	observer          Observer
	tags              []string
	fallbackTag       string
	link              string
	interval          time.Duration
	idleTimeout       time.Duration
	tolerance         int
	recoveryChecks    int
	interruptExternal bool
	leaf              adapter.Outbound
	fallbackConns     *interrupt.Group

	// Set by Start before the group carries traffic, then read-only.
	history  *urltest.HistoryStorage
	pause    pause.Manager
	members  []*member
	fallback adapter.Outbound

	access     sync.Mutex
	closed     bool
	view       atomic.Pointer[view]
	lastActive atomic.Int64
	wake       chan struct{}
	fresh      atomic.Bool
	cancel     context.CancelFunc
	done       chan struct{}
}

func New(ctx context.Context, _ adapter.Router, logger log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	if len(options.Outbounds) == 0 {
		return nil, E.New("missing tags")
	}
	for i, member := range options.Outbounds {
		if member == tag {
			return nil, E.New("group contains itself: ", tag)
		}
		if slices.Contains(options.Outbounds[:i], member) {
			return nil, E.New("duplicate tag: ", member)
		}
	}
	switch options.Strategy {
	case "", StrategyConsistentHashing:
	default:
		return nil, E.New("unknown strategy: ", options.Strategy)
	}
	if options.Fallback == tag {
		return nil, E.New("group is its own fallback: ", tag)
	}
	if options.URL != "" {
		link, err := url.Parse(options.URL)
		if err != nil || link.Hostname() == "" || (link.Scheme != "http" && link.Scheme != "https") {
			return nil, E.New("url must be an absolute HTTP(S) URL")
		}
	}
	interval, idleTimeout := time.Duration(options.Interval), time.Duration(options.IdleTimeout)
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval < 0 || interval > idleTimeout {
		return nil, E.New("interval must be positive and no greater than idle_timeout")
	}
	recoveryChecks := int(options.RecoveryChecks)
	if recoveryChecks == 0 {
		recoveryChecks = defaultRecoveryChecks
	}
	observer, _ := ctx.Value(observerKey{}).(Observer)
	g := &Group{
		// Members are start dependencies. The fallback is not, so two groups can
		// name each other as fallback; dials guard that loop at run time.
		Adapter:           outbound.NewAdapter(Type, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:               ctx,
		logger:            logger,
		manager:           service.FromContext[adapter.OutboundManager](ctx),
		observer:          observer,
		tags:              options.Outbounds,
		fallbackTag:       options.Fallback,
		link:              options.URL,
		interval:          interval,
		idleTimeout:       idleTimeout,
		tolerance:         int(options.Tolerance),
		recoveryChecks:    recoveryChecks,
		interruptExternal: options.InterruptExistConnections,
		fallbackConns:     interrupt.NewGroup(),
		wake:              make(chan struct{}, 1),
	}
	g.leaf = leaf{g}
	g.lastActive.Store(time.Now().UnixNano())
	return g, nil
}

func (g *Group) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	switch stage {
	case adapter.StartStateStart:
		history := service.PtrFromContext[urltest.HistoryStorage](g.ctx)
		if history == nil {
			return E.New("missing URL test history storage")
		}
		outbounds := make([]adapter.Outbound, 0, len(g.tags))
		for i, tag := range g.tags {
			detour, loaded := g.manager.Outbound(tag)
			if !loaded {
				return E.New("outbound ", i, " not found: ", tag)
			}
			outbounds = append(outbounds, detour)
		}
		var fallback adapter.Outbound
		if g.fallbackTag != "" {
			var loaded bool
			if fallback, loaded = g.manager.Outbound(g.fallbackTag); !loaded {
				return E.New("fallback not found: ", g.fallbackTag)
			}
		}
		g.bind(history, service.FromContext[pause.Manager](g.ctx), outbounds, fallback)
	case adapter.StartStateStarted:
		g.run(scope.Context())
		scope.Add(g.Close)
	}
	return nil
}

func (g *Group) bind(history *urltest.HistoryStorage, pauseManager pause.Manager, outbounds []adapter.Outbound, fallback adapter.Outbound) {
	g.history, g.pause, g.fallback = history, pauseManager, fallback
	g.members = make([]*member, len(outbounds))
	for i, detour := range outbounds {
		g.members[i] = &member{index: i, tag: detour.Tag(), outbound: detour, hash: mix(stringHash(detour.Tag())), conns: interrupt.NewGroup(), healthy: true}
	}
	g.access.Lock()
	g.publish()
	g.access.Unlock()
}

func (g *Group) Close() error {
	g.access.Lock()
	g.closed = true
	g.access.Unlock()
	if g.cancel != nil {
		g.cancel()
		<-g.done
	}
	return nil
}

func (g *Group) All() []string {
	return g.tags
}

// Selected is what the router follows to find the outbound that dials, so it
// returns the site-hashing leaf rather than a member. Current names a member
// for display.
func (g *Group) Selected(string) adapter.Outbound {
	return g.leaf
}

// Current names the member that represents the group for display: the
// fastest healthy member, or the fallback while it is in use.
func (g *Group) Current() string {
	if v := g.view.Load(); v != nil {
		return v.selected
	}
	return g.tags[0]
}

// The router attaches its side of every connection. Member failures close
// connections through the outbound side wrapped at dial time instead, so the
// group only records activity here.
func (g *Group) AttachConnection(io.Closer) func() {
	g.touch()
	return func() {}
}

func (g *Group) References() []string {
	references := slices.Clone(g.tags)
	if v := g.view.Load(); v != nil && v.fallback {
		references = append(references, g.fallbackTag)
	}
	return references
}

func (g *Group) URLTest(ctx context.Context) (map[string]uint16, error) {
	if g.view.Load() == nil {
		return nil, E.New("outbound/", Type, "[", g.Tag(), "] is not started")
	}
	return g.probe(ctx, false), nil
}

func (g *Group) PerformUpdateCheck() {
	g.refreshDelays()
}

func (g *Group) InterfaceUpdated(context.Context) {
	if g.paused() {
		return
	}
	// Failures seen on the previous network say little about the new one.
	g.fresh.Store(true)
	g.request()
}

func (g *Group) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	var conn net.Conn
	conns, err := g.open(ctx, N.NetworkName(network), destination, func(ctx context.Context, detour adapter.Outbound) (err error) {
		conn, err = detour.DialContext(ctx, network, destination)
		return
	})
	if err != nil {
		return nil, err
	}
	return conns.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (g *Group) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	var conn net.PacketConn
	conns, err := g.open(ctx, N.NetworkUDP, destination, func(ctx context.Context, detour adapter.Outbound) (err error) {
		conn, err = detour.ListenPacket(ctx, destination)
		return
	})
	if err != nil {
		return nil, err
	}
	return conns.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

// open dials through the members ranked for the destination site. It moves on
// only when a dial fails before any connection exists, so the caller has not
// handed application data to an outbound yet and nothing can be sent twice.
func (g *Group) open(ctx context.Context, network string, destination M.Socksaddr, dial func(context.Context, adapter.Outbound) error) (*interrupt.Group, error) {
	if network != N.NetworkTCP && network != N.NetworkUDP {
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	if g.view.Load() == nil {
		return nil, E.New("outbound/", Type, "[", g.Tag(), "] is not started")
	}
	g.touch()
	ranked := rank(g.members, siteKey(ctx, destination))
	tried := make([]bool, len(g.members)+1)
	var lastErr error
	for range maxAttempts {
		target, ok := g.next(ctx, network, ranked, tried)
		if !ok {
			break
		}
		dialContext, detour, conns := ctx, g.fallback, g.fallbackConns
		if target < len(g.members) {
			detour, conns = g.members[target].outbound, g.members[target].conns
		} else {
			dialContext = withVisited(ctx, g.Tag())
		}
		tried[target] = true
		err := dial(dialContext, detour)
		if err == nil {
			return conns, nil
		}
		if ctx.Err() != nil {
			// The caller gave up, e.g. a parallel dial that lost its race.
			return nil, err
		}
		lastErr = E.Cause(err, "outbound/", detour.Type(), "[", detour.Tag(), "]")
		if target < len(g.members) {
			g.fail(g.members[target], err)
		}
	}
	if lastErr == nil {
		lastErr = E.New("no outbound in ", g.Tag(), " supports ", network)
	}
	return nil, lastErr
}

// next returns the index of the next outbound to try, where len(members)
// stands for the fallback: healthy members in site order, then the fallback,
// then any remaining member as a last resort. It rereads the view, so members
// that failed earlier in this dial are no longer preferred.
func (g *Group) next(ctx context.Context, network string, ranked []*member, tried []bool) (int, bool) {
	v := g.view.Load()
	eligible := v.eligibleTCP
	if network == N.NetworkUDP {
		eligible = v.eligibleUDP
	}
	for _, m := range ranked {
		if !tried[m.index] && eligible[m.index] {
			return m.index, true
		}
	}
	if g.fallback != nil && !tried[len(g.members)] && !visited(ctx, g.Tag()) && !visited(ctx, g.fallbackTag) && supportsNetwork(g.fallback, network) {
		return len(g.members), true
	}
	for _, m := range ranked {
		if !tried[m.index] && supportsNetwork(m.outbound, network) {
			return m.index, true
		}
	}
	return 0, false
}

func supportsNetwork(outbound adapter.Outbound, network string) bool {
	return common.Contains(outbound.Network(), network)
}

// Groups can name each other as fallback. A dial records each group that
// handed it to its fallback; within that dial no recorded group hands off
// again or receives a hand-off.
type visitedKey struct{}

func withVisited(ctx context.Context, tag string) context.Context {
	tags, _ := ctx.Value(visitedKey{}).([]string)
	return context.WithValue(ctx, visitedKey{}, append(slices.Clip(tags), tag))
}

func visited(ctx context.Context, tag string) bool {
	tags, _ := ctx.Value(visitedKey{}).([]string)
	return slices.Contains(tags, tag)
}

// leaf is the outbound the router dials for this group. sing-box resolves a
// group through Selected and dials the last outbound of the chain itself, so
// the destination-aware choice has to live in an outbound that is not a group.
type leaf struct{ group *Group }

func (l leaf) Type() string           { return Type }
func (l leaf) Tag() string            { return l.group.Tag() }
func (l leaf) Network() []string      { return l.group.Network() }
func (l leaf) Dependencies() []string { return nil }
func (l leaf) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return l.group.DialContext(ctx, network, destination)
}
func (l leaf) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return l.group.ListenPacket(ctx, destination)
}
