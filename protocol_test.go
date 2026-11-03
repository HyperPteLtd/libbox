package libbox

import (
	"context"
	"github.com/HyperPteLtd/libbox/internal/backend"
	"github.com/HyperPteLtd/libbox/internal/control"
	pb "github.com/HyperPteLtd/libbox/protocol/v1"
	"google.golang.org/protobuf/proto"
	"os"
	"runtime"
	"testing"
	"time"
)

func exchange(t *testing.T, s *MessageSession, request *pb.Request) *pb.Response {
	t.Helper()
	data, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var response pb.Response
	if err = proto.Unmarshal(s.HandleMessage(data), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProtocolVersion != 1 || response.Id != request.Id {
		t.Fatalf("correlation lost: %+v", &response)
	}
	return &response
}
func TestProtobufLifecycleAndVersion(t *testing.T) {
	s := NewMessageSession(nil)
	defer s.Close()
	bad := exchange(t, s, &pb.Request{ProtocolVersion: 2, Id: 8, Method: &pb.Request_GetStatus{GetStatus: &pb.Empty{}}})
	if bad.GetError().GetCode() != "unsupported_version" {
		t.Fatal(bad)
	}
	create := exchange(t, s, &pb.Request{ProtocolVersion: 1, Id: 9, Method: &pb.Request_CreateEngine{CreateEngine: &pb.EngineOptions{WorkingDirectory: t.TempDir()}}})
	if create.GetError() != nil {
		t.Fatal(create)
	}
	start := exchange(t, s, &pb.Request{ProtocolVersion: 1, Id: 10, Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: "{}"}}})
	if start.GetError() != nil {
		t.Fatal(start)
	}
	status := exchange(t, s, &pb.Request{ProtocolVersion: 1, Id: 11, Method: &pb.Request_GetStatus{GetStatus: &pb.Empty{}}})
	if status.GetStatus().GetState() != "running" || status.GetStatus().GetGeneration() != 1 {
		t.Fatal(status)
	}
	var result pb.Response
	if err := proto.Unmarshal(s.HandleMessage(make([]byte, control.MaxMessageBytes+1)), &result); err != nil || result.GetError().GetCode() != "invalid_argument" {
		t.Fatalf("bounds: %+v %v", &result, err)
	}
	if err := proto.Unmarshal(s.HandleMessage([]byte{0xff}), &result); err != nil || result.GetError() == nil {
		t.Fatal("malformed request accepted")
	}
	exchange(t, s, &pb.Request{ProtocolVersion: 1, Id: 12, Method: &pb.Request_DestroyEngine{DestroyEngine: &pb.Empty{}}})
}
func TestProtobufExpiredQueuedStartNeverRuns(t *testing.T) {
	built := 0
	e, err := newEngineOptions(testOptions(t), fakeFactory{build: func(context.Context, string) (backend.Runtime, error) { built++; return &fakeRuntime{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	s := NewMessageSession(nil)
	s.engine = e
	defer s.Close()
	e.op.Lock()
	request := &pb.Request{ProtocolVersion: 1, Id: 7, TimeoutMs: 20, Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: "{}"}}}
	data, _ := proto.Marshal(request)
	done := make(chan []byte, 1)
	go func() { done <- s.HandleMessage(data) }()
	select {
	case response := <-done:
		var result pb.Response
		_ = proto.Unmarshal(response, &result)
		if result.GetError().GetCode() != "canceled" {
			e.op.Unlock()
			t.Fatal(&result)
		}
	case <-time.After(time.Second):
		e.op.Unlock()
		t.Fatal("queue admission ignored deadline")
	}
	e.op.Unlock()
	if built != 0 {
		t.Fatal("expired mutation ran later")
	}
	result := exchange(t, s, &pb.Request{ProtocolVersion: 1, Id: 8, DeadlineUnixMs: time.Now().Add(-time.Second).UnixMilli(), Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: "{}"}}})
	if result.GetError().GetCode() != "canceled" || built != 0 {
		t.Fatal(result)
	}
}
func TestSuccessfulStartOutlivesRequestDeadline(t *testing.T) {
	var lifetime context.Context
	e, err := newEngineOptions(testOptions(t), fakeFactory{build: func(ctx context.Context, _ string) (backend.Runtime, error) {
		lifetime = ctx
		return &fakeRuntime{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := NewMessageSession(nil)
	s.engine = e
	defer s.Close()
	result := exchange(t, s, &pb.Request{ProtocolVersion: 1, TimeoutMs: 15, Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: "{}"}}})
	if result.GetError() != nil {
		t.Fatal(result)
	}
	select {
	case <-lifetime.Done():
		t.Fatal("request timeout killed published runtime")
	case <-time.After(35 * time.Millisecond):
	}
}
func TestWindowsExampleConfiguration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows native TUN configuration is platform-specific")
	}
	data, err := os.ReadFile("examples/windows/tun.json")
	if err != nil {
		t.Fatal(err)
	}
	s := NewMessageSession(nil)
	defer s.Close()
	sessionCall(t, s, &pb.Request{Method: &pb.Request_CreateEngine{CreateEngine: &pb.EngineOptions{WorkingDirectory: t.TempDir()}}})
	result := sessionCall(t, s, &pb.Request{Method: &pb.Request_CheckConfig{CheckConfig: &pb.Config{ConfigJson: string(data)}}})
	if result.GetError() != nil {
		t.Fatal(result)
	}
}
