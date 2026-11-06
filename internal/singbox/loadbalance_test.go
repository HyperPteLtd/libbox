package singbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HyperPteLtd/libbox/internal/contract"
)

// connectProxy is a member server. It relays CONNECT requests for real
// addresses, which health checks use, and answers requests for *.example
// sites with its own name, so a client learns which member carried it.
type connectProxy struct {
	name     string
	listener net.Listener
	mu       sync.Mutex
	sites    int
}

func newConnectProxy(t *testing.T, name string) *connectProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &connectProxy{name: name, listener: listener}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go p.serve(conn)
		}
	}()
	return p
}

func (p *connectProxy) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	request, err := http.ReadRequest(reader)
	if err != nil || request.Method != http.MethodConnect {
		return
	}
	host, _, _ := net.SplitHostPort(request.Host)
	if strings.HasSuffix(host, ".example") {
		p.mu.Lock()
		p.sites++
		p.mu.Unlock()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		// Wait for the client so the name is not read as part of the reply.
		if _, err = reader.ReadByte(); err == nil {
			_, _ = io.WriteString(conn, p.name)
		}
		return
	}
	upstream, err := net.Dial("tcp", request.Host)
	if err != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer upstream.Close()
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	go func() { _, _ = io.Copy(upstream, reader); upstream.Close() }()
	_, _ = io.Copy(conn, upstream)
}

func (p *connectProxy) port() int { return p.listener.Addr().(*net.TCPAddr).Port }

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// memberFor opens a CONNECT tunnel to site through the mixed inbound and
// returns the name of the member that served it.
func memberFor(t *testing.T, inbound int, site string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inbound), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	target := site + ":443"
	if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT %s: %v %v", site, response, err)
	}
	if _, err = conn.Write([]byte{'?'}); err != nil {
		t.Fatal(err)
	}
	name, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(name)
}

func startRuntime(t *testing.T, config string, emit contract.Emit) *runtime {
	t.Helper()
	directory := t.TempDir()
	options := contract.Options{WorkingDirectory: directory, TempDirectory: directory, NetworkMode: "native", EventBuffer: 64}
	if err := (Factory{}).Check(context.Background(), config, options); err != nil {
		t.Fatal(err)
	}
	core, err := (Factory{}).Build(context.Background(), config, options, contract.Hooks{}, contract.Network{DefaultInterface: -1}, emit)
	if err != nil {
		t.Fatal(err)
	}
	r := core.(*runtime)
	t.Cleanup(func() { _ = r.Close() })
	if err = r.Start(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLoadBalanceRoutesConnectionsBySite(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer probe.Close()
	proxies := []*connectProxy{newConnectProxy(t, "jp-1"), newConnectProxy(t, "jp-2"), newConnectProxy(t, "jp-3")}
	inbound := freePort(t)
	config := fmt.Sprintf(`{
		"log": {"disabled": true},
		"inbounds": [{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": %d}],
		"outbounds": [
			{"type": "loadbalance", "tag": "JP", "outbounds": ["jp-1", "jp-2", "jp-3"], "strategy": "consistent-hashing", "url": %q, "interval": "1h", "idle_timeout": "2h"},
			{"type": "http", "tag": "jp-1", "server": "127.0.0.1", "server_port": %d},
			{"type": "http", "tag": "jp-2", "server": "127.0.0.1", "server_port": %d},
			{"type": "http", "tag": "jp-3", "server": "127.0.0.1", "server_port": %d}
		],
		"route": {"final": "JP"}
	}`, inbound, probe.URL, proxies[0].port(), proxies[1].port(), proxies[2].port())
	r := startRuntime(t, config, func(string, any) {})

	deadline := time.Now().Add(10 * time.Second)
	var listed contract.Group
	for {
		groups := r.Groups()
		if len(groups) == 1 && len(groups[0].Outbounds) == 3 && !slices.ContainsFunc(groups[0].Outbounds, func(it contract.Outbound) bool { return it.DelayMS == nil }) {
			listed = groups[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("members were not checked: %+v", groups)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if listed.Tag != "JP" || listed.Type != "loadbalance" || listed.Selectable || !slices.Contains([]string{"jp-1", "jp-2", "jp-3"}, listed.Selected) {
		t.Fatalf("unexpected group listing: %+v", listed)
	}
	if delay, err := r.URLTest(context.Background(), "JP", probe.URL); err != nil || delay < 0 {
		t.Fatalf("group URL test: %d %v", delay, err)
	}

	sites := make([]string, 24)
	assigned := map[string]string{}
	used := map[string]bool{}
	for i := range sites {
		sites[i] = fmt.Sprintf("www.site-%d.example", i)
		assigned[sites[i]] = memberFor(t, inbound, sites[i])
		used[assigned[sites[i]]] = true
	}
	if len(used) != 3 {
		t.Fatalf("routed sites were not spread: %v", assigned)
	}
	for _, site := range sites[:6] {
		if again := memberFor(t, inbound, site); again != assigned[site] {
			t.Fatalf("%s moved from %s to %s", site, assigned[site], again)
		}
	}
	if other := memberFor(t, inbound, "cdn.site-0.example"); other != assigned["www.site-0.example"] {
		t.Fatal("subdomains of one site used different members")
	}

	// A dead member hands only its own sites to the others.
	proxies[1].listener.Close()
	for _, site := range sites {
		member := memberFor(t, inbound, site)
		if assigned[site] == "jp-2" && member == "jp-2" || assigned[site] != "jp-2" && member != assigned[site] {
			t.Fatalf("%s: assigned %s, now %s", site, assigned[site], member)
		}
	}
	if selected := r.Groups()[0].Selected; selected == "jp-2" {
		t.Fatal("failed member is still the representative")
	}
}

func TestLoadBalanceConfigIsValidated(t *testing.T) {
	directory := t.TempDir()
	options := contract.Options{WorkingDirectory: directory, TempDirectory: directory, NetworkMode: "native", EventBuffer: 64}
	for _, config := range []string{
		`{"outbounds":[{"type":"loadbalance","tag":"JP","outbounds":["a"],"strategy":"round-robin"},{"type":"direct","tag":"a"}]}`,
		`{"outbounds":[{"type":"loadbalance","tag":"JP","outbounds":["a"],"unknown_field":true},{"type":"direct","tag":"a"}]}`,
		`{"outbounds":[{"type":"loadbalance","tag":"JP","outbounds":[]}]}`,
	} {
		if err := (Factory{}).Check(context.Background(), config, options); err == nil {
			t.Errorf("accepted %s", config)
		}
	}
	// Member and fallback tags resolve when the core starts.
	for _, config := range []string{
		`{"log":{"disabled":true},"outbounds":[{"type":"loadbalance","tag":"JP","outbounds":["missing"]}]}`,
		`{"log":{"disabled":true},"outbounds":[{"type":"loadbalance","tag":"JP","outbounds":["a"],"fallback":"missing"},{"type":"direct","tag":"a"}]}`,
	} {
		core, err := (Factory{}).Build(context.Background(), config, options, contract.Hooks{}, contract.Network{DefaultInterface: -1}, func(string, any) {})
		if err != nil {
			t.Fatal(err)
		}
		if err = core.Start(); err == nil {
			t.Errorf("started %s", config)
		}
		_ = core.Close()
	}
}
