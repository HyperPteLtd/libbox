package bridge

import (
	"errors"
	pb "github.com/HyperPteLtd/libbox/protocol/v1"
	"google.golang.org/protobuf/proto"
	"sync"
	"testing"
	"time"
)

func TestSessionHandleLifetime(t *testing.T) {
	var registry Sessions
	handle := registry.New(nil)
	create, _ := proto.Marshal(&pb.Request{ProtocolVersion: 1, Id: 1, Method: &pb.Request_CreateEngine{CreateEngine: &pb.EngineOptions{WorkingDirectory: t.TempDir()}}})
	var result pb.Response
	if err := proto.Unmarshal(registry.Call(handle, create), &result); err != nil || result.GetError() != nil {
		t.Fatalf("%+v %v", &result, err)
	}
	query, _ := proto.Marshal(&pb.Request{ProtocolVersion: 1, Id: 7, Method: &pb.Request_GetStatus{GetStatus: &pb.Empty{}}})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				registry.Call(handle, query)
			}
		})
	}
	registry.Free(handle)
	wg.Wait()
	if err := proto.Unmarshal(registry.Call(handle, query), &result); err != nil || result.Id != 7 || result.GetError().GetCode() != "invalid_handle" {
		t.Fatalf("%+v %v", &result, err)
	}
	registry.Free(handle)
}

type blockingHost struct{ entered, release chan struct{} }

func (h *blockingHost) HandleHostMessage([]byte) ([]byte, error) {
	close(h.entered)
	<-h.release
	return nil, errors.New("test rejected socket protection")
}

func TestConcurrentFreeWaitsForHostCallback(t *testing.T) {
	var registry Sessions
	host := &blockingHost{make(chan struct{}), make(chan struct{})}
	var release sync.Once
	handle := registry.New(host)
	defer registry.Free(handle)
	defer release.Do(func() { close(host.release) })
	call := func(request *pb.Request) *pb.Response {
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
	for _, request := range []*pb.Request{
		{Method: &pb.Request_CreateEngine{CreateEngine: &pb.EngineOptions{WorkingDirectory: t.TempDir(), NetworkMode: pb.EngineOptions_HOST, HostCapabilities: []pb.EngineOptions_HostCapability{pb.EngineOptions_PROTECT_SOCKET}}}},
		{Method: &pb.Request_UpdateNetwork{UpdateNetwork: &pb.Network{DefaultInterface: 1, Interfaces: []*pb.Interface{{Index: 1, Name: "lo-test", Mtu: 1500, Addresses: []string{"127.0.0.1/8"}, Loopback: true}}}}},
		{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: `{"log":{"disabled":true},"outbounds":[{"type":"direct","tag":"direct"}]}`}}},
	} {
		if response := call(request); response.GetError() != nil {
			t.Fatal(response)
		}
	}
	testDone := make(chan struct{})
	go func() {
		defer close(testDone)
		call(&pb.Request{Method: &pb.Request_UrlTest{UrlTest: &pb.URLTest{Outbound: "direct", Url: "http://127.0.0.1:1/", TimeoutMs: 5000}}})
	}()
	select {
	case <-host.entered:
	case <-time.After(time.Second):
		t.Fatal("host callback not reached")
	}
	first := make(chan struct{})
	go func() { registry.Free(handle); close(first) }()
	deadline := time.Now().Add(time.Second)
	for {
		response := call(&pb.Request{Method: &pb.Request_GetInfo{GetInfo: &pb.Empty{}}})
		if response.GetError() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close admission did not latch")
		}
		time.Sleep(time.Millisecond)
	}
	second := make(chan struct{})
	go func() { registry.Free(handle); close(second) }()
	select {
	case <-second:
		t.Fatal("duplicate free returned while callback context was borrowed")
	case <-time.After(20 * time.Millisecond):
	}
	release.Do(func() { close(host.release) })
	for _, done := range []chan struct{}{first, second, testDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("callback cleanup did not drain")
		}
	}
}
