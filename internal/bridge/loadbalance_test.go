package bridge

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	pb "github.com/HyperPteLtd/libbox/protocol/v1"
	"google.golang.org/protobuf/proto"
)

func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func TestLoadBalanceGroupThroughProtobufControl(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer probe.Close()
	var registry Sessions
	handle := registry.New(nil)
	defer registry.Free(handle)
	call := func(request *pb.Request) *pb.Response {
		t.Helper()
		request.ProtocolVersion = 1
		data, err := proto.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		var response pb.Response
		if err = proto.Unmarshal(registry.Call(handle, data), &response); err != nil {
			t.Fatal(err)
		}
		return &response
	}
	if response := call(&pb.Request{Method: &pb.Request_CreateEngine{CreateEngine: &pb.EngineOptions{WorkingDirectory: t.TempDir()}}}); response.GetError() != nil {
		t.Fatal(response)
	}
	if info := call(&pb.Request{Method: &pb.Request_GetInfo{GetInfo: &pb.Empty{}}}).GetInfo(); !slices.Contains(info.GetCapabilities(), "loadbalance_group") {
		t.Fatalf("capability missing: %v", info.GetCapabilities())
	}

	// KR has no working member and falls back to JP, another loadbalance group.
	config := fmt.Sprintf(`{
		"log": {"disabled": true},
		"outbounds": [
			{"type": "loadbalance", "tag": "JP", "outbounds": ["jp-1", "jp-2", "jp-3"], "url": %[1]q, "interval": "1h", "idle_timeout": "2h", "tolerance": 150, "interrupt_exist_connections": true},
			{"type": "loadbalance", "tag": "KR", "outbounds": ["kr-1"], "url": %[1]q, "interval": "1h", "idle_timeout": "2h", "fallback": "JP"},
			{"type": "direct", "tag": "jp-1"},
			{"type": "direct", "tag": "jp-2"},
			{"type": "http", "tag": "jp-3", "server": "127.0.0.1", "server_port": %[2]d},
			{"type": "http", "tag": "kr-1", "server": "127.0.0.1", "server_port": %[2]d}
		],
		"route": {"final": "JP"}
	}`, probe.URL, closedPort(t))
	invalid := `{"outbounds":[{"type":"loadbalance","tag":"JP","outbounds":["a"],"strategy":"round-robin"},{"type":"direct","tag":"a"}]}`
	if response := call(&pb.Request{Method: &pb.Request_CheckConfig{CheckConfig: &pb.Config{ConfigJson: invalid}}}); response.GetError().GetCode() != "invalid_config" {
		t.Fatalf("invalid strategy accepted: %v", response)
	}
	for _, request := range []*pb.Request{
		{Method: &pb.Request_CheckConfig{CheckConfig: &pb.Config{ConfigJson: config}}},
		{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: config}}},
	} {
		if response := call(request); response.GetError() != nil {
			t.Fatal(response)
		}
	}
	defer call(&pb.Request{Method: &pb.Request_Stop{Stop: &pb.Empty{}}})

	health := map[string]*pb.GroupHealth{}
	deadline := time.Now().Add(10 * time.Second)
	for health["JP"] == nil || health["KR"] == nil {
		if time.Now().After(deadline) {
			t.Fatalf("missing group health events: %v", health)
		}
		event := call(&pb.Request{Method: &pb.Request_NextEvent{NextEvent: &pb.NextEvent{TimeoutMs: 1000}}}).GetEvent()
		if value := event.GetGroupHealth(); value != nil {
			health[value.Group] = value
		}
	}
	if jp := health["JP"]; !slices.Equal(jp.Healthy, []string{"jp-1", "jp-2"}) || !slices.Equal(jp.Unhealthy, []string{"jp-3"}) || jp.Fallback != "" {
		t.Fatalf("unexpected JP health: %v", jp)
	}
	if kr := health["KR"]; len(kr.Healthy) != 0 || kr.Fallback != "JP" {
		t.Fatalf("unexpected KR health: %v", kr)
	}

	groups := map[string]*pb.Group{}
	for _, group := range call(&pb.Request{Method: &pb.Request_ListGroups{ListGroups: &pb.Empty{}}}).GetGroups().GetGroups() {
		groups[group.Tag] = group
	}
	jp := groups["JP"]
	if jp.GetType() != "loadbalance" || jp.GetSelectable() || !slices.Contains([]string{"jp-1", "jp-2"}, jp.GetSelected()) || len(jp.GetOutbounds()) != 3 {
		t.Fatalf("unexpected JP listing: %v", jp)
	}
	for i, member := range jp.GetOutbounds() {
		if member.Tag != fmt.Sprintf("jp-%d", i+1) || (member.DelayMs == nil) != (member.Tag == "jp-3") {
			t.Fatalf("unexpected JP member: %v", member)
		}
	}
	if groups["KR"].GetSelected() != "JP" {
		t.Fatalf("KR does not show its fallback: %v", groups["KR"])
	}
	for _, tag := range []string{"JP", "KR"} {
		response := call(&pb.Request{Method: &pb.Request_UrlTest{UrlTest: &pb.URLTest{Outbound: tag, Url: probe.URL, TimeoutMs: 5000}}})
		if response.GetError() != nil || response.GetDelay() == nil {
			t.Fatalf("url_test %s: %v", tag, response)
		}
	}
}
