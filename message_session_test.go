package libbox

import (
	"context"
	"testing"

	"github.com/HyperPteLtd/libbox/internal/contract"
	pb "github.com/HyperPteLtd/libbox/protocol/v1"
	"google.golang.org/protobuf/proto"
)

type hostMessageFunc func([]byte) ([]byte, error)

func TestZeroSessionAndInvalidOptions(t *testing.T) {
	var zero MessageSession
	result := sessionCall(t, &zero, &pb.Request{Method: &pb.Request_GetInfo{GetInfo: &pb.Empty{}}})
	if result.GetError().GetCode() != "uninitialized" {
		t.Fatal(result)
	}
	for _, options := range []*pb.EngineOptions{{}, {WorkingDirectory: "relative"}, {WorkingDirectory: t.TempDir(), NetworkMode: 99}, {WorkingDirectory: t.TempDir(), MemoryProfile: 99}, {WorkingDirectory: t.TempDir(), MemoryLimitBytes: -1}, {WorkingDirectory: t.TempDir(), GcPercent: 2000}, {WorkingDirectory: t.TempDir(), EventBuffer: proto.Uint32(0)}} {
		s := NewMessageSession(nil)
		response := sessionCall(t, s, &pb.Request{Method: &pb.Request_CreateEngine{CreateEngine: options}})
		if response.GetError().GetCode() != "invalid_argument" {
			t.Fatal(response)
		}
		_ = s.Close()
	}
}

func (f hostMessageFunc) HandleHostMessage(data []byte) ([]byte, error) { return f(data) }
func sessionCall(t *testing.T, s *MessageSession, request *pb.Request) *pb.Response {
	t.Helper()
	request.ProtocolVersion = 1
	request.Id = 42
	data, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var response pb.Response
	if err = proto.Unmarshal(s.HandleMessage(data), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProtocolVersion != 1 || response.Id != 42 {
		t.Fatal(&response)
	}
	return &response
}
func TestMessageSessionUsesOnlyProtobuf(t *testing.T) {
	s := NewMessageSession(nil)
	defer s.Close()
	info := sessionCall(t, s, &pb.Request{Method: &pb.Request_GetInfo{GetInfo: &pb.Empty{}}})
	if info.GetInfo().GetApiVersion() != 1 {
		t.Fatal(info)
	}
	missing := sessionCall(t, s, &pb.Request{Method: &pb.Request_GetStatus{GetStatus: &pb.Empty{}}})
	if missing.GetError().GetCode() != "uninitialized" {
		t.Fatal(missing)
	}
	create := &pb.Request{Method: &pb.Request_CreateEngine{CreateEngine: &pb.EngineOptions{WorkingDirectory: t.TempDir()}}}
	if result := sessionCall(t, s, create); result.GetError() != nil {
		t.Fatal(result)
	}
	if result := sessionCall(t, s, create); result.GetError().GetCode() != "invalid_state" {
		t.Fatal(result)
	}
	if result := sessionCall(t, s, &pb.Request{Method: &pb.Request_Start{Start: &pb.Config{ConfigJson: `{"log":{"disabled":true},"outbounds":[{"type":"direct","tag":"direct"}]}`}}}); result.GetError() != nil {
		t.Fatal(result)
	}
	if result := sessionCall(t, s, &pb.Request{Method: &pb.Request_GetStatus{GetStatus: &pb.Empty{}}}); result.GetStatus().GetState() != "running" {
		t.Fatal(result)
	}
	if result := sessionCall(t, s, &pb.Request{Method: &pb.Request_DestroyEngine{DestroyEngine: &pb.Empty{}}}); result.GetError() != nil {
		t.Fatal(result)
	}
	if result := sessionCall(t, s, create); result.GetError() != nil {
		t.Fatal(result)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if result := sessionCall(t, s, create); result.GetError().GetCode() != "closed" {
		t.Fatal(result)
	}
}
func TestProtobufHostCallbackAndCorrelation(t *testing.T) {
	badID := false
	s := NewMessageSession(hostMessageFunc(func(data []byte) ([]byte, error) {
		var request pb.HostRequest
		if err := proto.Unmarshal(data, &request); err != nil {
			return nil, err
		}
		if request.ProtocolVersion != 1 || request.TimeoutMs == 0 {
			t.Fatal(&request)
		}
		response := &pb.HostResponse{ProtocolVersion: 1, Id: request.Id}
		switch request.Method.(type) {
		case *pb.HostRequest_OpenTun:
			if request.GetOpenTun().GetMtu() != 1500 {
				t.Fatal("lost typed TUN options")
			}
			response.Result = &pb.HostResponse_Tun{Tun: &pb.TunDescriptor{Fd: 25}}
		case *pb.HostRequest_ProtectSocket:
			response.Result = &pb.HostResponse_Empty{Empty: &pb.Empty{}}
		case *pb.HostRequest_FindConnectionOwner:
			response.Result = &pb.HostResponse_ConnectionOwner{ConnectionOwner: &pb.ConnectionOwner{UserId: 123, PackageNames: []string{"example"}}}
		}
		if badID {
			response.Id++
		}
		return proto.Marshal(response)
	}))
	defer s.Close()
	options := &pb.EngineOptions{WorkingDirectory: t.TempDir(), NetworkMode: pb.EngineOptions_HOST, HostCapabilities: []pb.EngineOptions_HostCapability{pb.EngineOptions_OPEN_TUN, pb.EngineOptions_PROTECT_SOCKET, pb.EngineOptions_CONNECTION_OWNER}}
	if result := sessionCall(t, s, &pb.Request{Method: &pb.Request_CreateEngine{CreateEngine: options}}); result.GetError() != nil {
		t.Fatal(result)
	}
	s.mu.RLock()
	e := s.engine
	s.mu.RUnlock()
	fd, err := e.hooks.OpenTun(contract.TunOptions{MTU: 1500})
	if err != nil || fd != 25 {
		t.Fatalf("fd=%d err=%v", fd, err)
	}
	if err = e.hooks.ProtectSocket(12); err != nil {
		t.Fatal(err)
	}
	owner, err := e.hooks.FindConnectionOwner(contract.ConnectionRequest{Protocol: 6})
	if err != nil || owner.UserID != 123 {
		t.Fatalf("%+v %v", owner, err)
	}
	badID = true
	if err = e.hooks.ProtectSocket(12); err == nil {
		t.Fatal("accepted mismatched response id")
	}
}
func TestMessageSessionRejectsInvalidCapability(t *testing.T) {
	s := NewMessageSession(nil)
	defer s.Close()
	options := &pb.EngineOptions{WorkingDirectory: t.TempDir(), NetworkMode: pb.EngineOptions_HOST, HostCapabilities: []pb.EngineOptions_HostCapability{pb.EngineOptions_OPEN_TUN}}
	if result := sessionCall(t, s, &pb.Request{Method: &pb.Request_CreateEngine{CreateEngine: options}}); result.GetError().GetCode() != "invalid_argument" {
		t.Fatal(result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (sessionControl{s}).Create(ctx, &pb.EngineOptions{WorkingDirectory: t.TempDir()}); err == nil {
		t.Fatal("expired create mutated session")
	}
}
