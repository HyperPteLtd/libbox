package loadbalance

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/protocol/group"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service/pause"
)

type member struct {
	index    int
	tag      string
	outbound adapter.Outbound
	hash     uint64
	conns    *interrupt.Group

	// Guarded by Group.access.
	healthy   bool
	successes int
	measured  bool
	delay     uint16
}

// view is the immutable routing state that dials read without locking. A new
// view is published after every change to member state.
type view struct {
	eligible []bool
	fallback bool
	selected string
	delay    int
	health   Health
}

// publish must be called with access held.
func (g *Group) publish() *view {
	v := &view{eligible: make([]bool, len(g.members)), delay: -1, health: Health{Group: g.Tag(), Healthy: []string{}, Unhealthy: []string{}}}
	for _, m := range g.members {
		if !m.healthy {
			v.health.Unhealthy = append(v.health.Unhealthy, m.tag)
			continue
		}
		v.health.Healthy = append(v.health.Healthy, m.tag)
		if m.measured && (v.delay < 0 || int(m.delay) < v.delay) {
			v.delay, v.selected = int(m.delay), m.tag
		}
	}
	for _, m := range g.members {
		v.eligible[m.index] = m.healthy && (g.tolerance == 0 || v.delay < 0 || !m.measured || int(m.delay) <= v.delay+g.tolerance)
	}
	switch {
	case len(v.health.Healthy) == 0 && g.fallback != nil:
		v.fallback, v.selected = true, g.fallbackTag
		v.health.Fallback = g.fallbackTag
	case v.selected == "" && len(v.health.Healthy) > 0:
		v.selected = v.health.Healthy[0]
	case v.selected == "":
		v.selected = g.tags[0]
	}
	g.view.Store(v)
	return v
}

// apply records one completed check round. A member that fails a check
// leaves at once. A failed member rejoins only after recoveryChecks
// consecutive successful rounds, so a flapping member does not move sites back
// and forth; after a network change the first success is enough.
func (g *Group) apply(results map[string]uint16, fresh bool) {
	var failed []*member
	g.access.Lock()
	if g.closed {
		g.access.Unlock()
		return
	}
	changed := false
	for _, m := range g.members {
		delay, ok := results[m.tag]
		if !ok {
			m.measured, m.successes = false, 0
			if m.healthy {
				m.healthy, changed = false, true
				failed = append(failed, m)
				g.logger.Warn("outbound ", m.tag, " failed its health check")
			}
			continue
		}
		m.measured, m.delay = true, delay
		if m.healthy {
			continue
		}
		m.successes++
		if fresh || m.successes >= g.recoveryChecks {
			m.healthy, m.successes, changed = true, 0, true
			g.logger.Info("outbound ", m.tag, " recovered")
		}
	}
	previous := g.view.Load()
	current := g.publish()
	if changed {
		g.notify(previous, current)
	}
	g.access.Unlock()
	g.settle(previous, current, failed)
}

// fail removes a member right after a dial through it failed, without
// waiting for the next check, and starts a check round to confirm.
func (g *Group) fail(m *member, err error) {
	g.access.Lock()
	if g.closed || !m.healthy {
		g.access.Unlock()
		return
	}
	m.healthy, m.measured, m.successes = false, false, 0
	g.logger.Warn("outbound ", m.tag, " failed: ", err)
	previous := g.view.Load()
	current := g.publish()
	g.notify(previous, current)
	g.access.Unlock()
	g.history.DeleteURLTestHistory(m.tag)
	g.settle(previous, current, []*member{m})
	g.request()
}

// refreshDelays adopts delays measured elsewhere, such as a host URL test of
// a member, without changing health.
func (g *Group) refreshDelays() {
	g.access.Lock()
	if g.closed || g.view.Load() == nil {
		g.access.Unlock()
		return
	}
	for _, m := range g.members {
		if history := g.history.LoadURLTestHistory(group.RealTag(m.outbound, N.NetworkTCP)); history != nil && m.healthy {
			m.measured, m.delay = true, history.Delay
		}
	}
	previous := g.view.Load()
	current := g.publish()
	g.access.Unlock()
	g.settle(previous, current, nil)
}

// notify must be called with access held so observers see changes in order.
func (g *Group) notify(previous, current *view) {
	if previous != nil && previous.fallback != current.fallback {
		if current.fallback {
			g.logger.Warn("no healthy outbound, using fallback ", g.fallbackTag)
		} else {
			g.logger.Info("healthy outbound available, leaving fallback ", g.fallbackTag)
		}
	}
	if g.observer != nil {
		g.observer(current.health)
	}
}

// settle applies the side effects of a published view outside the lock.
func (g *Group) settle(previous, current *view, failed []*member) {
	for _, m := range failed {
		m.conns.Interrupt(g.interruptExternal)
	}
	if previous != nil && previous.fallback && !current.fallback {
		g.fallbackConns.Interrupt(g.interruptExternal)
	}
	// The group's own history entry is the delay of its representative member.
	// A parent urltest group reads it through RealTag; a group that has no
	// healthy member reports none. Concurrent changes may settle out of order,
	// so write the latest view rather than this one.
	if latest := g.view.Load(); latest.delay >= 0 {
		g.history.StoreURLTestHistory(g.Tag(), &adapter.URLTestHistory{Time: time.Now(), Delay: uint16(latest.delay)})
	} else if g.history.LoadURLTestHistory(g.Tag()) != nil {
		g.history.DeleteURLTestHistory(g.Tag())
	}
}

func (g *Group) probe(ctx context.Context, fresh bool) map[string]uint16 {
	outbounds := make([]adapter.Outbound, len(g.members))
	for i, m := range g.members {
		outbounds[i] = m.outbound
	}
	results := group.URLTestOutbounds(ctx, g.manager, g.history, g.logger, outbounds, g.link, g.interval, true)
	if ctx.Err() == nil {
		g.apply(results, fresh)
	}
	return results
}

// run checks all members once, then every interval while the group carries
// traffic, plus whenever a dial fails or the network changes.
func (g *Group) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	g.cancel, g.done = cancel, make(chan struct{})
	go g.loop(ctx)
}

func (g *Group) loop(ctx context.Context) {
	defer close(g.done)
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	if g.pause != nil {
		callback := pause.RegisterTicker(g.pause, ticker, g.interval, nil)
		defer g.pause.UnregisterCallback(callback)
	}
	if !g.paused() {
		g.probe(ctx, false)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.wake:
		case <-ticker.C:
			if g.idle() {
				continue
			}
		}
		if !g.paused() {
			g.probe(ctx, g.fresh.Swap(false))
		}
	}
}

func (g *Group) request() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// touch records traffic. Checks pause after idle_timeout without traffic, so
// the first connection after that refreshes member health.
func (g *Group) touch() {
	now := time.Now().UnixNano()
	if time.Duration(now-g.lastActive.Swap(now)) > g.idleTimeout {
		g.request()
	}
}

func (g *Group) idle() bool {
	return time.Since(time.Unix(0, g.lastActive.Load())) > g.idleTimeout
}

func (g *Group) paused() bool {
	return g.pause != nil && (g.pause.IsDevicePaused() || g.pause.IsNetworkPaused())
}
