package libbox_test

import (
	"encoding/json"
	"fmt"
	libbox "github.com/HyperPteLtd/libbox"
	pb "github.com/HyperPteLtd/libbox/protocol/v1"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type client struct {
	s   *libbox.MessageSession
	seq atomic.Uint64
}

func (c *client) call(request *pb.Request) (*pb.Response, error) {
	request.ProtocolVersion = 1
	request.Id = c.seq.Add(1)
	data, err := proto.Marshal(request)
	if err != nil {
		return nil, err
	}
	var response pb.Response
	if err = proto.Unmarshal(c.s.HandleMessage(data), &response); err != nil {
		return nil, err
	}
	if response.ProtocolVersion != 1 || response.Id != request.Id {
		return nil, fmt.Errorf("protocol mismatch")
	}
	if failure := response.GetError(); failure != nil {
		return &response, fmt.Errorf("libbox:%s: %s", failure.Code, failure.Message)
	}
	return &response, nil
}
func (c *client) must(t *testing.T, request *pb.Request) *pb.Response {
	t.Helper()
	response, err := c.call(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func newClient(t *testing.T, host libbox.HostMessageHandler, options *pb.EngineOptions) *client {
	t.Helper()
	if options == nil {
		options = &pb.EngineOptions{}
	}
	options.WorkingDirectory = t.TempDir()
	c := &client{s: libbox.NewMessageSession(host)}
	t.Cleanup(func() {
		if err := c.s.Close(); err != nil {
			t.Error(err)
		}
	})
	c.must(t, &pb.Request{Method: &pb.Request_CreateEngine{CreateEngine: options}})
	return c
}
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}
func proxyConfig(port int) string {
	value := map[string]any{
		"log":       map[string]any{"disabled": true},
		"inbounds":  []any{map[string]any{"type": "mixed", "tag": "mixed", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []any{map[string]any{"type": "selector", "tag": "proxy", "outbounds": []string{"one", "two"}}, map[string]any{"type": "direct", "tag": "one"}, map[string]any{"type": "direct", "tag": "two"}},
		"route":     map[string]any{"final": "proxy"}}
	data, _ := json.Marshal(value)
	return string(data)
}
func getThroughProxy(t *testing.T, port int, target string) {
	t.Helper()
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "through-sing-box" {
		t.Fatalf("body=%s error=%v", body, err)
	}
}
func localServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "through-sing-box") }))
}
func TestRealProxyLifecycleGroupsAndReload(t *testing.T) {
	server := localServer()
	defer server.Close()
	c := newClient(t, nil, nil)
	port := freePort(t)
	config := proxyConfig(port)
	c.must(t, &pb.Request{Method: &pb.Request_CheckConfig{CheckConfig: &pb.Config{ConfigJson: config}}})
	c.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: config}}})
	getThroughProxy(t, port, server.URL)
	groups := c.must(t, &pb.Request{Method: &pb.Request_ListGroups{ListGroups: &pb.Empty{}}}).GetGroups().Groups
	if len(groups) != 1 || groups[0].Selected != "one" {
		t.Fatal(groups)
	}
	c.must(t, &pb.Request{Method: &pb.Request_SelectOutbound{SelectOutbound: &pb.SelectOutbound{Group: "proxy", Outbound: "two"}}})
	if _, err := c.call(&pb.Request{Method: &pb.Request_SelectOutbound{SelectOutbound: &pb.SelectOutbound{Group: "proxy", Outbound: "missing"}}}); err == nil {
		t.Fatal("accepted unknown outbound")
	}
	if value := c.must(t, &pb.Request{Method: &pb.Request_ListGroups{ListGroups: &pb.Empty{}}}).GetGroups().Groups[0].Selected; value != "two" {
		t.Fatal(value)
	}
	c.must(t, &pb.Request{Method: &pb.Request_UrlTest{UrlTest: &pb.URLTest{Outbound: "two", Url: server.URL, TimeoutMs: 2000}}})
	outbounds := c.must(t, &pb.Request{Method: &pb.Request_ListOutbounds{ListOutbounds: &pb.Empty{}}}).GetOutbounds().Outbounds
	measured := false
	for _, outbound := range outbounds {
		if outbound.Tag == "two" && outbound.DelayMs != nil {
			measured = true
		}
	}
	if !measured {
		t.Fatal("missing test history")
	}
	if _, err := c.call(&pb.Request{Method: &pb.Request_Reload{Reload: &pb.Config{ConfigJson: "["}}}); err == nil {
		t.Fatal("accepted invalid config")
	}
	getThroughProxy(t, port, server.URL)
	c.must(t, &pb.Request{Method: &pb.Request_Reload{Reload: &pb.Config{ConfigJson: config}}})
	getThroughProxy(t, port, server.URL)
	c.must(t, &pb.Request{Method: &pb.Request_Stop{Stop: &pb.Empty{}}})
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	c.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: config}}})
	getThroughProxy(t, port, server.URL)
}
func TestStartInvalidConfigIsRecoverable(t *testing.T) {
	c := newClient(t, nil, nil)
	for _, config := range []string{"null", "["} {
		if _, err := c.call(&pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: config}}}); err == nil {
			t.Fatal(config)
		}
	}
	c.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: "{}"}}})
}
func TestIndependentInstances(t *testing.T) {
	server := localServer()
	defer server.Close()
	first, second := newClient(t, nil, nil), newClient(t, nil, nil)
	firstPort := freePort(t)
	first.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: proxyConfig(firstPort)}}})
	secondPort := freePort(t)
	second.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: proxyConfig(secondPort)}}})
	first.must(t, &pb.Request{Method: &pb.Request_DestroyEngine{DestroyEngine: &pb.Empty{}}})
	getThroughProxy(t, secondPort, server.URL)
}
func TestRealReloadPortConflictRestoresProxy(t *testing.T) {
	server := localServer()
	defer server.Close()
	c := newClient(t, nil, nil)
	port := freePort(t)
	c.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: proxyConfig(port)}}})
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	_, err = c.call(&pb.Request{Method: &pb.Request_Reload{Reload: &pb.Config{ConfigJson: proxyConfig(occupied.Addr().(*net.TCPAddr).Port)}}})
	if err == nil || !strings.Contains(err.Error(), "reload_failed") {
		t.Fatal(err)
	}
	getThroughProxy(t, port, server.URL)
}

type rejectingHost struct{ calls atomic.Int32 }

func (h *rejectingHost) HandleHostMessage(data []byte) ([]byte, error) {
	var request pb.HostRequest
	if err := proto.Unmarshal(data, &request); err != nil {
		return nil, err
	}
	h.calls.Add(1)
	return proto.Marshal(&pb.HostResponse{ProtocolVersion: 1, Id: request.Id, Result: &pb.HostResponse_Error{Error: &pb.Error{Code: "blocked", Message: "protect denied"}}})
}
func TestHostSocketProtectionFailsClosed(t *testing.T) {
	host := &rejectingHost{}
	c := newClient(t, host, &pb.EngineOptions{NetworkMode: pb.EngineOptions_HOST, HostCapabilities: []pb.EngineOptions_HostCapability{pb.EngineOptions_PROTECT_SOCKET}})
	c.must(t, &pb.Request{Method: &pb.Request_UpdateNetwork{UpdateNetwork: &pb.Network{DefaultInterface: 1, Interfaces: []*pb.Interface{{Index: 1, Name: "lo-test", Mtu: 1500, Addresses: []string{"127.0.0.1/8"}, Type: "other", Loopback: true}}}}})
	c.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: "{}"}}})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unprotected request reached server") }))
	defer server.Close()
	outbounds := c.must(t, &pb.Request{Method: &pb.Request_ListOutbounds{ListOutbounds: &pb.Empty{}}}).GetOutbounds().Outbounds
	if len(outbounds) == 0 {
		t.Fatal("no default outbound")
	}
	_, err := c.call(&pb.Request{Method: &pb.Request_UrlTest{UrlTest: &pb.URLTest{Outbound: outbounds[0].Tag, Url: server.URL, TimeoutMs: 1000}}})
	if err == nil || host.calls.Load() == 0 {
		t.Fatalf("protection bypassed: calls=%d error=%v", host.calls.Load(), err)
	}
}

func TestModeSwitchUsesNewGenerationUnderTraffic(t *testing.T) {
	server := localServer()
	defer server.Close()
	c := newClient(t, nil, nil)
	port := freePort(t)
	var config map[string]any
	if err := json.Unmarshal([]byte(proxyConfig(port)), &config); err != nil {
		t.Fatal(err)
	}
	config["route"] = map[string]any{"final": "proxy", "rules": []any{map[string]any{"clash_mode": "Global", "outbound": "two"}}}
	data, _ := json.Marshal(config)
	c.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: string(data)}}})
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: time.Second}
	done := make(chan struct{})
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			response, err := httpClient.Get(server.URL)
			if err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
		}
	})
	for _, mode := range []string{"Global", "Rule", "global"} {
		c.must(t, &pb.Request{Method: &pb.Request_SetMode{SetMode: &pb.SetMode{Mode: mode}}})
	}
	close(done)
	workers.Wait()
	status := c.must(t, &pb.Request{Method: &pb.Request_GetStatus{GetStatus: &pb.Empty{}}}).GetStatus()
	if status.Mode != "Global" || status.Generation != 4 {
		t.Fatal(status)
	}
	getThroughProxy(t, port, server.URL)
	if _, err := c.call(&pb.Request{Method: &pb.Request_SetMode{SetMode: &pb.SetMode{Mode: "invalid"}}}); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestModeSwitchChangesLiveRouting(t *testing.T) {
	server := localServer()
	defer server.Close()
	c := newClient(t, nil, nil)
	port := freePort(t)
	var config map[string]any
	if err := json.Unmarshal([]byte(proxyConfig(port)), &config); err != nil {
		t.Fatal(err)
	}
	config["route"] = map[string]any{"final": "proxy", "rules": []any{map[string]any{"clash_mode": "Global", "action": "reject"}}}
	data, _ := json.Marshal(config)
	c.must(t, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: string(data)}}})
	getThroughProxy(t, port, server.URL)
	c.must(t, &pb.Request{Method: &pb.Request_SetMode{SetMode: &pb.SetMode{Mode: "Global"}}})
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: time.Second}).Get(server.URL)
	if err == nil {
		response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatal("mode changed in status but the live routing rule did not reject")
		}
	}
	c.must(t, &pb.Request{Method: &pb.Request_SetMode{SetMode: &pb.SetMode{Mode: "Rule"}}})
	getThroughProxy(t, port, server.URL)
}
