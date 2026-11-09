package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/HyperPteLtd/libbox/internal/contract"
	mDNS "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestLogPayloadIsBoundedAndUTF8(t *testing.T) {
	var received contract.Log
	w := writer{emit: func(_ string, value any) { received = value.(contract.Log) }}
	w.WriteMessage(log.LevelInfo, strings.Repeat("字", 10000)+"\xff")
	if !received.Truncated || len(received.Message) > 16<<10 || !utf8.ValidString(received.Message) {
		t.Fatalf("invalid bounded log: length=%d truncated=%v", len(received.Message), received.Truncated)
	}
	w.WriteMessage(log.LevelInfo, "short")
	if received.Truncated || received.Message != "short" {
		t.Fatal(received)
	}
}

func TestFailedTunDupPreservesBorrowedDescriptor(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "linux" && goruntime.GOOS != "android" {
		t.Skip("FD TUN is unavailable")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	for range 20 {
		if fd, _, err := duplicateTun(int(reader.Fd())); err == nil {
			closeDescriptor(fd)
			t.Fatal("accepted pipe as TUN")
		}
	}
	if _, err = writer.Write([]byte{42}); err != nil {
		t.Fatal(err)
	}
	var data [1]byte
	if _, err = reader.Read(data[:]); err != nil || data[0] != 42 {
		t.Fatalf("borrowed descriptor damaged: %v", err)
	}
}

func TestCallbackLifetimeBarrier(t *testing.T) {
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	p := newPlatform(contract.Options{}, contract.Hooks{ProtectSocket: func(int32) error { calls++; close(entered); <-release; return nil }}, contract.Network{})
	done := make(chan error, 1)
	go func() { done <- p.AutoDetectInterfaceControl(1) }()
	<-entered
	go func() { p.closeCallbacks(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("closed while a callback still uses host context")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-closed
	if err := p.AutoDetectInterfaceControl(1); err == nil {
		t.Fatal("late callback was accepted")
	}
	if calls != 1 {
		t.Fatalf("unexpected callback count: %d", calls)
	}
}

func TestHostConfigRejectsSocketProtectionBypass(t *testing.T) {
	for _, raw := range []string{
		`{"route":{"default_interface":"eth0"}}`,
		`{"outbounds":[{"type":"direct","inet4_bind_address":"127.0.0.1"}]}`,
		`{"dns":{"servers":[{"type":"udp","bind_interface":"eth0"}]}}`,
	} {
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		if err := checkHostConfig(value); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

type dnsDialer struct {
	mu      sync.Mutex
	udp     string
	tcp     string
	queries []string
	block   bool
}

func (d *dnsDialer) DialContext(ctx context.Context, network string, target M.Socksaddr) (net.Conn, error) {
	d.mu.Lock()
	d.queries = append(d.queries, network+":"+target.String())
	block := d.block
	d.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	address := d.udp
	if network == "tcp" {
		address = d.tcp
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
func (*dnsDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unexpected ListenPacket")
}

func testDNS(t *testing.T, d *dnsDialer) (*hostDNS, *platform) {
	t.Helper()
	p := newPlatform(contract.Options{}, contract.Hooks{}, contract.Network{DefaultInterface: 1, Interfaces: []contract.Interface{{Index: 1, Name: "test", MTU: 1500, DNSServers: []string{"127.0.0.2"}}}})
	preferred, err := local.NewPreferredDomainResolver(context.Background(), log.NewNOPFactory().Logger(), option.LocalDNSServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &hostDNS{platform: p, dialer: d, preferred: preferred}, p
}
func TestHostDNSUpdateAndTCPFallback(t *testing.T) {
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		udp.Close()
		t.Fatal(err)
	}
	udpReady, tcpReady := make(chan struct{}), make(chan struct{})
	udpServer := &mDNS.Server{PacketConn: udp, NotifyStartedFunc: func() { close(udpReady) }, Handler: mDNS.HandlerFunc(func(w mDNS.ResponseWriter, r *mDNS.Msg) {
		reply := new(mDNS.Msg)
		reply.SetReply(r)
		reply.Truncated = true
		_ = w.WriteMsg(reply)
	})}
	tcpServer := &mDNS.Server{Listener: tcp, NotifyStartedFunc: func() { close(tcpReady) }, Handler: mDNS.HandlerFunc(func(w mDNS.ResponseWriter, r *mDNS.Msg) {
		reply := new(mDNS.Msg)
		reply.SetReply(r)
		reply.Answer = []mDNS.RR{&mDNS.A{Hdr: mDNS.RR_Header{Name: r.Question[0].Name, Rrtype: mDNS.TypeA, Class: mDNS.ClassINET, Ttl: 60}, A: net.IPv4(127, 0, 0, 1)}}
		_ = w.WriteMsg(reply)
	})}
	go func() { _ = udpServer.ActivateAndServe() }()
	go func() { _ = tcpServer.ActivateAndServe() }()
	<-udpReady
	<-tcpReady
	defer udpServer.Shutdown()
	defer tcpServer.Shutdown()
	dialer := &dnsDialer{udp: udp.LocalAddr().String(), tcp: tcp.Addr().String()}
	h, p := testDNS(t, dialer)
	question := new(mDNS.Msg)
	question.SetQuestion("sdk.test.", mDNS.TypeA)
	response, err := h.Exchange(context.Background(), question)
	if err != nil || len(response.Answer) != 1 || response.Truncated {
		t.Fatalf("DNS fallback failed: %v %v", response, err)
	}
	if err = p.update(contract.Network{DefaultInterface: 2, Interfaces: []contract.Interface{{Index: 2, Name: "next", MTU: 1500, DNSServers: []string{"127.0.0.3"}, DNSSearchDomains: []string{"sdk.test"}}}}); err != nil {
		t.Fatal(err)
	}
	interfaces, err := p.NetworkInterfaces()
	if err != nil || len(interfaces) != 1 || !slices.Equal(interfaces[0].DNSSearchDomains, []string{"sdk.test"}) {
		t.Fatalf("host search domains not forwarded to the core: %+v %v", interfaces, err)
	}
	interfaces[0].DNSSearchDomains[0] = "mutated.test"
	if _, err = h.Exchange(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	dialer.mu.Lock()
	queried := append([]string(nil), dialer.queries...)
	dialer.mu.Unlock()
	if len(queried) != 4 || queried[0] != "udp:127.0.0.2:53" || queried[1] != "tcp:127.0.0.2:53" || queried[2] != "udp:127.0.0.3:53" {
		t.Fatalf("wrong DNS destinations: %v", queried)
	}
	if h.ServerAddresses()[0] != netip.MustParseAddr("127.0.0.3") {
		t.Fatal("stale resolver metadata")
	}
	domains := h.SearchDomains()
	if len(domains) != 1 || domains[0] != "sdk.test" {
		t.Fatal("lost search domains in the release adapter", domains)
	}
	domains[0] = "mutated.test"
	if values := h.Environment(); len(values) != 2 || values[1] != "sdk.test" {
		t.Fatal("DNS environment lost or aliased the host snapshot", values)
	}
	if err = p.update(contract.Network{DefaultInterface: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Exchange(context.Background(), question); err == nil {
		t.Fatal("offline DNS silently fell back to process resolver")
	}
}

func TestReleaseConnectionOwnerMapping(t *testing.T) {
	owner := contract.ConnectionOwner{UserID: 42, ProcessID: 100, UserName: "test", ProcessPaths: []string{"/primary", "/secondary"}, PackageNames: []string{"test.package"}}
	p := newPlatform(contract.Options{}, contract.Hooks{FindConnectionOwner: func(contract.ConnectionRequest) (contract.ConnectionOwner, error) { return owner, nil }}, contract.Network{})
	result, err := p.FindConnectionOwner(&adapter.FindConnectionOwnerRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.UserId != owner.UserID || result.ProcessID != owner.ProcessID || result.UserName != owner.UserName || !slices.Equal(result.ProcessPaths, owner.ProcessPaths) || !slices.Equal(result.PackageNames, owner.PackageNames) {
		t.Fatalf("incorrect release owner mapping: %+v", result)
	}
	result.PackageNames[0] = "mutated"
	result.ProcessPaths[0] = "mutated"
	if owner.PackageNames[0] != "test.package" || owner.ProcessPaths[0] != "/primary" {
		t.Fatal("owner details alias the host result")
	}
}

func TestHostDNSCancellation(t *testing.T) {
	h, _ := testDNS(t, &dnsDialer{block: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	question := new(mDNS.Msg)
	question.SetQuestion("sdk.test.", mDNS.TypeA)
	if _, err := h.Exchange(ctx, question); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("not canceled: %v", err)
	}
	if _, err := h.Exchange(context.Background(), new(mDNS.Msg)); err == nil {
		t.Fatal("accepted questionless DNS message")
	}
}
