package loadbalance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// fakeOutbound dials one local address whatever the destination, so tests
// observe which member carried a connection without touching the network.
type fakeOutbound struct {
	tag      string
	target   string
	network  []string
	fail     atomic.Bool
	failNext atomic.Bool
	block    bool
	dials    atomic.Int32
	mu       sync.Mutex
	seen     []M.Socksaddr
}

func (f *fakeOutbound) Type() string           { return "fake" }
func (f *fakeOutbound) Tag() string            { return f.tag }
func (f *fakeOutbound) Dependencies() []string { return nil }
func (f *fakeOutbound) Network() []string {
	if f.network != nil {
		return f.network
	}
	return []string{N.NetworkTCP, N.NetworkUDP}
}
func (f *fakeOutbound) attempt(ctx context.Context, destination M.Socksaddr) error {
	f.dials.Add(1)
	f.mu.Lock()
	f.seen = append(f.seen, destination)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.fail.Load() || f.failNext.Swap(false) {
		return errors.New(f.tag + " is unreachable")
	}
	return nil
}
func (f *fakeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if err := f.attempt(ctx, destination); err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, N.NetworkTCP, f.target)
}
func (f *fakeOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if err := f.attempt(ctx, destination); err != nil {
		return nil, err
	}
	return net.ListenPacket(N.NetworkUDP, "127.0.0.1:0")
}

// recorder is a TCP server that keeps every byte it receives.
type recorder struct {
	listener net.Listener
	mu       sync.Mutex
	conns    int
	data     []byte
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	listener, err := net.Listen(N.NetworkTCP, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &recorder{listener: listener}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.conns++
			r.mu.Unlock()
			go func() {
				data, _ := io.ReadAll(conn)
				conn.Close()
				r.mu.Lock()
				r.data = append(r.data, data...)
				r.mu.Unlock()
			}()
		}
	}()
	return r
}

func (r *recorder) snapshot() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns, string(r.data)
}

func newProbeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(server.Close)
	return server
}

type events struct {
	mu     sync.Mutex
	values []Health
}

func (e *events) observe(health Health) {
	e.mu.Lock()
	e.values = append(e.values, health)
	e.mu.Unlock()
}

func (e *events) all() []Health {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.values)
}

func newGroup(t *testing.T, options Options, members []*fakeOutbound, fallback *fakeOutbound, observer Observer) *Group {
	t.Helper()
	return newNamedGroup(t, "JP", options, members, fallback, observer)
}

func newNamedGroup(t *testing.T, tag string, options Options, members []*fakeOutbound, fallback *fakeOutbound, observer Observer) *Group {
	t.Helper()
	ctx := context.Background()
	if observer != nil {
		ctx = ContextWithObserver(ctx, observer)
	}
	outbounds := make([]adapter.Outbound, len(members))
	for i, m := range members {
		options.Outbounds = append(options.Outbounds, m.tag)
		outbounds[i] = m
	}
	var fallbackOutbound adapter.Outbound
	if fallback != nil {
		options.Fallback, fallbackOutbound = fallback.tag, fallback
	}
	created, err := New(ctx, nil, log.NewNOPFactory().Logger(), tag, options)
	if err != nil {
		t.Fatal(err)
	}
	g := created.(*Group)
	g.bind(urltest.NewHistoryStorage(), nil, outbounds, fallbackOutbound)
	t.Cleanup(func() { g.Close() })
	return g
}

// siteRankedAs finds a domain whose preference order starts with tags.
func siteRankedAs(t *testing.T, g *Group, tags ...string) M.Socksaddr {
	t.Helper()
	for i := range 10000 {
		destination := M.ParseSocksaddrHostPort(fmt.Sprintf("site-%d.example", i), 443)
		ranked := rank(g.members, siteKey(context.Background(), destination))
		if slices.EqualFunc(ranked[:len(tags)], tags, func(m *member, tag string) bool { return m.tag == tag }) {
			return destination
		}
	}
	t.Fatalf("no site ranked %v", tags)
	return M.Socksaddr{}
}

func healthyTags(g *Group) []string {
	return g.view.Load().health.Healthy
}

func TestDialKeepsSitesOnTheirMember(t *testing.T) {
	server := newRecorder(t)
	members := []*fakeOutbound{{tag: "jp-1", target: server.listener.Addr().String()}, {tag: "jp-2", target: server.listener.Addr().String()}, {tag: "jp-3", target: server.listener.Addr().String()}}
	g := newGroup(t, Options{}, members, nil, nil)
	used := map[string]bool{}
	for i := range 60 {
		destination := M.ParseSocksaddrHostPort(fmt.Sprintf("www.site-%d.example", i), 443)
		expected := rank(g.members, siteKey(context.Background(), destination))[0].outbound.(*fakeOutbound)
		for range 2 {
			before := expected.dials.Load()
			conn, err := g.DialContext(context.Background(), N.NetworkTCP, destination)
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			if expected.dials.Load() != before+1 {
				t.Fatalf("%s did not use its ranked member %s", destination, expected.tag)
			}
		}
		used[expected.tag] = true
	}
	if len(used) != 3 {
		t.Fatalf("sites were not spread: %v", used)
	}
	// Subdomains of one registrable domain share a member.
	a := rank(g.members, siteKey(context.Background(), M.ParseSocksaddrHostPort("img.shop.example", 443)))[0]
	b := rank(g.members, siteKey(context.Background(), M.ParseSocksaddrHostPort("api.shop.example", 443)))[0]
	if a != b {
		t.Fatal("subdomains of one site were split")
	}
}

func TestDialFailoverNeverDuplicatesData(t *testing.T) {
	servers := []*recorder{newRecorder(t), newRecorder(t), newRecorder(t)}
	members := []*fakeOutbound{{tag: "jp-1", target: servers[0].listener.Addr().String()}, {tag: "jp-2", target: servers[1].listener.Addr().String()}, {tag: "jp-3", target: servers[2].listener.Addr().String()}}
	var seen events
	g := newGroup(t, Options{}, members, nil, seen.observe)
	destination := siteRankedAs(t, g, "jp-1", "jp-2")
	members[0].fail.Store(true)
	conn, err := g.DialContext(context.Background(), N.NetworkTCP, destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, data := servers[1].snapshot(); data == "payload" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("payload did not reach the next member")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if members[0].dials.Load() != 1 || members[1].dials.Load() != 1 || members[2].dials.Load() != 0 {
		t.Fatalf("unexpected attempts: %d %d %d", members[0].dials.Load(), members[1].dials.Load(), members[2].dials.Load())
	}
	for i, server := range []*recorder{servers[0], servers[2]} {
		if conns, data := server.snapshot(); conns != 0 || data != "" {
			t.Fatalf("server %d received %d connections and %q", i, conns, data)
		}
	}
	if values := seen.all(); len(values) != 1 || !slices.Equal(values[0].Unhealthy, []string{"jp-1"}) {
		t.Fatalf("missing passive failure event: %+v", values)
	}

	// The failed member is skipped without another attempt.
	if conn, err = g.DialContext(context.Background(), N.NetworkTCP, destination); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if members[0].dials.Load() != 1 || members[1].dials.Load() != 2 {
		t.Fatal("dial retried a failed member")
	}
}

func TestCanceledDialIsNotAMemberFailure(t *testing.T) {
	members := []*fakeOutbound{{tag: "jp-1", block: true}, {tag: "jp-2"}}
	g := newGroup(t, Options{}, members, nil, nil)
	destination := siteRankedAs(t, g, "jp-1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.DialContext(ctx, N.NetworkTCP, destination); err == nil {
		t.Fatal("canceled dial succeeded")
	}
	if !slices.Equal(healthyTags(g), []string{"jp-1", "jp-2"}) || members[1].dials.Load() != 0 {
		t.Fatal("canceled dial marked the member down or retried")
	}
}

func TestRecoveryNeedsConsecutiveChecks(t *testing.T) {
	members := []*fakeOutbound{{tag: "jp-1"}, {tag: "jp-2"}}
	var seen events
	g := newGroup(t, Options{RecoveryChecks: 2}, members, nil, seen.observe)
	g.apply(map[string]uint16{"jp-2": 20}, false)
	if !slices.Equal(healthyTags(g), []string{"jp-2"}) {
		t.Fatal("failed check kept the member")
	}
	g.apply(map[string]uint16{"jp-1": 10, "jp-2": 20}, false)
	g.apply(map[string]uint16{"jp-2": 20}, false)
	g.apply(map[string]uint16{"jp-1": 10, "jp-2": 20}, false)
	if !slices.Equal(healthyTags(g), []string{"jp-2"}) {
		t.Fatal("member rejoined before consecutive successful checks")
	}
	g.apply(map[string]uint16{"jp-1": 10, "jp-2": 20}, false)
	if !slices.Equal(healthyTags(g), []string{"jp-1", "jp-2"}) || g.Current() != "jp-1" {
		t.Fatalf("member did not rejoin: %v %s", healthyTags(g), g.Current())
	}
	values := seen.all()
	if len(values) != 2 || !slices.Equal(values[0].Unhealthy, []string{"jp-1"}) || len(values[1].Unhealthy) != 0 {
		t.Fatalf("unexpected events: %+v", values)
	}

	// A network change resets the evidence: the first success is enough.
	g.apply(map[string]uint16{"jp-2": 20}, false)
	g.apply(map[string]uint16{"jp-1": 10, "jp-2": 20}, true)
	if !slices.Equal(healthyTags(g), []string{"jp-1", "jp-2"}) {
		t.Fatal("fresh check did not restore the member")
	}
}

func TestFallbackStartsAndStops(t *testing.T) {
	server := newRecorder(t)
	members := []*fakeOutbound{{tag: "jp-1", target: server.listener.Addr().String()}, {tag: "jp-2", target: server.listener.Addr().String()}}
	fallback := &fakeOutbound{tag: "HK", target: server.listener.Addr().String()}
	var seen events
	g := newGroup(t, Options{InterruptExistConnections: true}, members, fallback, seen.observe)
	g.apply(map[string]uint16{}, false)
	if values := seen.all(); len(values) != 1 || values[0].Fallback != "HK" || !slices.Equal(values[0].Unhealthy, []string{"jp-1", "jp-2"}) {
		t.Fatalf("fallback was not reported: %+v", values)
	}
	if g.Current() != "HK" || !slices.Contains(g.References(), "HK") {
		t.Fatal("fallback is not the current outbound")
	}
	ctx := interrupt.ContextWithIsExternalConnection(context.Background(), true)
	conn, err := g.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("example.com", 443))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if fallback.dials.Load() != 1 || members[0].dials.Load()+members[1].dials.Load() != 0 {
		t.Fatal("dial did not go to the fallback")
	}
	g.apply(map[string]uint16{"jp-1": 10, "jp-2": 20}, false)
	if len(seen.all()) != 1 || !isOpen(conn) {
		t.Fatal("fallback ended before members recovered")
	}
	g.apply(map[string]uint16{"jp-1": 10, "jp-2": 20}, false)
	values := seen.all()
	if len(values) != 2 || values[1].Fallback != "" || !slices.Equal(values[1].Healthy, []string{"jp-1", "jp-2"}) {
		t.Fatalf("fallback release was not reported: %+v", values)
	}
	if isOpen(conn) {
		t.Fatal("connection through the fallback was not interrupted")
	}
	if slices.Contains(g.References(), "HK") {
		t.Fatal("idle fallback is still referenced")
	}
}

// isOpen reports whether a dialed connection is still usable. A closed one
// fails at once; an open one only reaches the read deadline.
func isOpen(conn net.Conn) bool {
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, err := conn.Read(make([]byte, 1))
	return errors.Is(err, os.ErrDeadlineExceeded)
}

func TestFailedMemberConnectionsAreInterrupted(t *testing.T) {
	for _, interruptExternal := range []bool{true, false} {
		server := newRecorder(t)
		members := []*fakeOutbound{{tag: "jp-1", target: server.listener.Addr().String()}, {tag: "jp-2", target: server.listener.Addr().String()}}
		g := newGroup(t, Options{InterruptExistConnections: interruptExternal}, members, nil, nil)
		destination := siteRankedAs(t, g, "jp-1")
		external, err := g.DialContext(interrupt.ContextWithIsExternalConnection(context.Background(), true), N.NetworkTCP, destination)
		if err != nil {
			t.Fatal(err)
		}
		internal, err := g.DialContext(context.Background(), N.NetworkTCP, destination)
		if err != nil {
			t.Fatal(err)
		}
		other, err := g.DialContext(context.Background(), N.NetworkTCP, siteRankedAs(t, g, "jp-2"))
		if err != nil {
			t.Fatal(err)
		}
		g.apply(map[string]uint16{"jp-2": 20}, false)
		if isOpen(external) == interruptExternal || isOpen(internal) || !isOpen(other) {
			t.Fatalf("interrupt_exist_connections=%v closed the wrong connections", interruptExternal)
		}
		external.Close()
		internal.Close()
		other.Close()
	}
}

func TestListenPacketUsesTheSiteMember(t *testing.T) {
	members := []*fakeOutbound{{tag: "jp-1"}, {tag: "jp-2"}, {tag: "jp-3"}, {tag: "tcp-only", network: []string{N.NetworkTCP}}}
	g := newGroup(t, Options{}, members, nil, nil)
	used := map[string]bool{}
	for i := range 40 {
		destination := M.ParseSocksaddr(fmt.Sprintf("198.51.100.%d:443", i+1))
		var expected *fakeOutbound
		for _, m := range rank(g.members, siteKey(context.Background(), destination)) {
			if m.tag != "tcp-only" {
				expected = m.outbound.(*fakeOutbound)
				break
			}
		}
		before := expected.dials.Load()
		conn, err := g.ListenPacket(context.Background(), destination)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if expected.dials.Load() != before+1 {
			t.Fatalf("%s did not use %s", destination, expected.tag)
		}
		used[expected.tag] = true
	}
	if len(used) != 3 || members[3].dials.Load() != 0 {
		t.Fatalf("UDP was not spread over UDP members: %v", used)
	}

	destination := siteRankedAs(t, g, "jp-2", "jp-3")
	members[1].fail.Store(true)
	conn, err := g.ListenPacket(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if members[1].dials.Load() == 0 || slices.Contains(healthyTags(g), "jp-2") {
		t.Fatal("UDP failure did not fail over")
	}
}

func TestToleranceLimitsEligibleMembers(t *testing.T) {
	members := []*fakeOutbound{{tag: "jp-1"}, {tag: "jp-2"}, {tag: "jp-3"}}
	g := newGroup(t, Options{Tolerance: 100}, members, nil, nil)
	g.apply(map[string]uint16{"jp-1": 50, "jp-2": 400, "jp-3": 120}, false)
	if v := g.view.Load(); !slices.Equal(v.eligible, []bool{true, false, true}) || v.selected != "jp-1" {
		t.Fatalf("unexpected eligibility: %+v", v)
	}
	// The slow member is still healthy and takes over when the others fail.
	g.apply(map[string]uint16{"jp-2": 400}, false)
	if v := g.view.Load(); !slices.Equal(v.eligible, []bool{false, true, false}) {
		t.Fatalf("slow member did not take over: %+v", v)
	}
}

func TestMutualFallbackTerminates(t *testing.T) {
	first := &fakeOutbound{tag: "jp-1"}
	second := &fakeOutbound{tag: "hk-1"}
	first.fail.Store(true)
	second.fail.Store(true)
	jp := newNamedGroup(t, "JP", Options{}, []*fakeOutbound{first}, nil, nil)
	hk := newNamedGroup(t, "HK", Options{}, []*fakeOutbound{second}, nil, nil)
	jp.fallback, jp.fallbackTag = hk, "HK"
	hk.fallback, hk.fallbackTag = jp, "JP"
	jp.apply(map[string]uint16{}, false)
	hk.apply(map[string]uint16{}, false)
	if _, err := jp.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.com", 443)); err == nil {
		t.Fatal("dial succeeded without a working outbound")
	}
	if first.dials.Load() != 1 || second.dials.Load() != 1 {
		t.Fatalf("unexpected attempts: %d %d", first.dials.Load(), second.dials.Load())
	}
}

func TestDialFailureStartsACheck(t *testing.T) {
	probe := newProbeServer(t)
	target := probe.Listener.Addr().String()
	members := []*fakeOutbound{{tag: "jp-1", target: target}, {tag: "jp-2", target: target}}
	var seen events
	g := newGroup(t, Options{URL: probe.URL, Interval: badoption.Duration(time.Hour), IdleTimeout: badoption.Duration(2 * time.Hour), RecoveryChecks: 1}, members, nil, seen.observe)
	g.run(context.Background())
	waitFor(t, "initial check", func() bool { return g.view.Load().delay >= 0 })
	// Only the dial fails; the check that follows it succeeds.
	members[0].failNext.Store(true)
	if conn, err := g.DialContext(context.Background(), N.NetworkTCP, siteRankedAs(t, g, "jp-1")); err != nil {
		t.Fatal(err)
	} else {
		conn.Close()
	}
	// The hourly ticker cannot explain a recovery within this test.
	waitFor(t, "check after the dial failure", func() bool { return len(seen.all()) == 2 })
	if values := seen.all(); !slices.Equal(values[1].Healthy, []string{"jp-1", "jp-2"}) {
		t.Fatalf("unexpected events: %+v", values)
	}
	if history := g.history.LoadURLTestHistory("JP"); history == nil {
		t.Fatal("group delay was not recorded")
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOptionsValidation(t *testing.T) {
	logger := log.NewNOPFactory().Logger()
	for _, options := range []Options{
		{},
		{Outbounds: []string{"a", "a"}},
		{Outbounds: []string{"JP"}},
		{Outbounds: []string{"a"}, Strategy: "round-robin"},
		{Outbounds: []string{"a"}, URL: "ftp://example.com/"},
		{Outbounds: []string{"a"}, Fallback: "JP"},
		{Outbounds: []string{"a"}, Interval: badoption.Duration(time.Hour), IdleTimeout: badoption.Duration(time.Minute)},
	} {
		if _, err := New(context.Background(), nil, logger, "JP", options); err == nil {
			t.Errorf("accepted %+v", options)
		}
	}
	if _, err := New(context.Background(), nil, logger, "JP", Options{Outbounds: []string{"a"}, Strategy: StrategyConsistentHashing, URL: "https://example.com/generate_204"}); err != nil {
		t.Fatal(err)
	}
}
