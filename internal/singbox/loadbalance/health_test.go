package loadbalance

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	N "github.com/sagernet/sing/common/network"
)

func TestToleranceUsesSupportedNetworkMembers(t *testing.T) {
	server := newRecorder(t)
	address := server.listener.Addr().String()
	tcp := &fakeOutbound{tag: "tcp-only", target: address, network: []string{N.NetworkTCP}}
	dual := &fakeOutbound{tag: "dual", target: address}
	fallback := &fakeOutbound{tag: "fallback", target: address}
	g := newGroup(t, Options{Tolerance: 100}, []*fakeOutbound{tcp, dual}, fallback, nil)
	g.apply(map[string]uint16{"tcp-only": 10, "dual": 500}, false)
	destination := siteRankedAs(t, g, "dual")

	conn, err := g.DialContext(context.Background(), N.NetworkTCP, destination)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if tcp.dials.Load() != 1 || dual.dials.Load() != 0 {
		t.Fatal("TCP tolerance did not exclude the slower member")
	}
	packet, err := g.ListenPacket(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	packet.Close()
	conn, err = g.DialContext(context.Background(), N.NetworkUDP, destination)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if dual.dials.Load() != 2 || tcp.dials.Load() != 1 || fallback.dials.Load() != 0 {
		t.Fatalf("UDP bypassed its healthy member: tcp=%d dual=%d fallback=%d", tcp.dials.Load(), dual.dials.Load(), fallback.dials.Load())
	}

	g.apply(map[string]uint16{"tcp-only": 10}, false)
	packet, err = g.ListenPacket(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	packet.Close()
	if fallback.dials.Load() != 1 {
		t.Fatal("UDP did not use fallback when its only member failed")
	}
}

type healthTestManager struct {
	adapter.OutboundManager
	values map[string]adapter.Outbound
}

func (m healthTestManager) Outbound(tag string) (adapter.Outbound, bool) {
	v, ok := m.values[tag]
	return v, ok
}

func parentGroup(t *testing.T, child *Group, link string) *Group {
	t.Helper()
	created, err := New(context.Background(), nil, log.NewNOPFactory().Logger(), "parent", Options{Outbounds: []string{child.Tag()}, URL: link})
	if err != nil {
		t.Fatal(err)
	}
	g := created.(*Group)
	values := map[string]adapter.Outbound{child.Tag(): child}
	for _, m := range child.members {
		values[m.tag] = m.outbound
	}
	g.manager = healthTestManager{values: values}
	g.bind(child.history, nil, []adapter.Outbound{child}, nil)
	t.Cleanup(func() { g.Close() })
	return g
}

func TestNestedProbeRejectsStaleSuccess(t *testing.T) {
	probe := newProbeServer(t)
	m := &fakeOutbound{tag: "node", target: probe.Listener.Addr().String()}
	child := newNamedGroup(t, "child", Options{URL: probe.URL}, []*fakeOutbound{m}, nil, nil)
	child.probe(context.Background(), false)
	if child.history.LoadURLTestHistory("child") == nil {
		t.Fatal("missing initial successful check")
	}
	parent := parentGroup(t, child, probe.URL)
	m.fail.Store(true)
	result := parent.probe(context.Background(), false)
	if _, ok := result["child"]; ok || len(healthyTags(parent)) != 0 {
		t.Fatalf("stale child history hid a failed check: results=%v healthy=%v", result, healthyTags(parent))
	}
}

func TestNestedProbeMeasuresRecoveredPathAndFallback(t *testing.T) {
	for _, useFallback := range []bool{false, true} {
		name := "recovered member"
		if useFallback {
			name = "working fallback"
		}
		t.Run(name, func(t *testing.T) {
			probe := newProbeServer(t)
			m := &fakeOutbound{tag: "node", target: probe.Listener.Addr().String()}
			var fallback *fakeOutbound
			if useFallback {
				fallback = &fakeOutbound{tag: "fallback", target: probe.Listener.Addr().String()}
			}
			child := newNamedGroup(t, "child", Options{URL: probe.URL}, []*fakeOutbound{m}, fallback, nil)
			m.fail.Store(true)
			child.probe(context.Background(), false)
			if !useFallback {
				m.fail.Store(false)
			}
			parent := parentGroup(t, child, probe.URL)
			results := parent.probe(context.Background(), false)
			if _, ok := results["child"]; !ok || !slices.Equal(healthyTags(parent), []string{"child"}) {
				t.Fatalf("working nested path was not measured: %v %v", results, healthyTags(parent))
			}
			if useFallback && fallback.dials.Load() != 1 {
				t.Fatal("nested fallback was not actually probed")
			}
			if child.history.LoadURLTestHistory("child") != nil {
				t.Fatal("parent probe overwrote the child's representative history")
			}
		})
	}
}

func TestNestedProbeKeepsChildRepresentativeHistory(t *testing.T) {
	probe := newProbeServer(t)
	m := &fakeOutbound{tag: "node", target: probe.Listener.Addr().String()}
	child := newNamedGroup(t, "child", Options{URL: probe.URL}, []*fakeOutbound{m}, nil, nil)
	child.apply(map[string]uint16{"node": 65000}, false)
	before := child.history.LoadURLTestHistory("child")
	parent := parentGroup(t, child, probe.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := parent.probe(ctx, false)
	if delay, ok := results["child"]; !ok || delay == 65000 || m.dials.Load() != 1 {
		t.Fatalf("probe used cached history instead of dialing: %v", results)
	}
	if child.history.LoadURLTestHistory("child") != before {
		t.Fatal("child representative history was replaced by the parent's measurement")
	}
}

func TestCanceledProbePreservesHealthAndHistory(t *testing.T) {
	probe := newProbeServer(t)
	m := &fakeOutbound{tag: "node", target: probe.Listener.Addr().String()}
	g := newGroup(t, Options{URL: probe.URL}, []*fakeOutbound{m}, nil, nil)
	g.probe(context.Background(), false)
	before := g.history.LoadURLTestHistory(m.tag)
	if before == nil {
		t.Fatal("missing initial member history")
	}
	m.block = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	g.probe(ctx, false)
	if !slices.Equal(healthyTags(g), []string{m.tag}) || g.history.LoadURLTestHistory(m.tag) != before {
		t.Fatal("cancellation changed member health or history")
	}
}
