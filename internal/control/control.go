// Package control implements SDK-owned protobuf messages independently of both
// transport and the upstream core. Internal DTOs never escape the wire boundary.
package control

import (
	"context"
	"github.com/HyperPteLtd/libbox/internal/contract"
	pb "github.com/HyperPteLtd/libbox/protocol/v1"
	"google.golang.org/protobuf/proto"
	"strings"
	"time"
)

const Version = 1
const MaxMessageBytes = 4 << 20
const MaxHostResponseBytes = 64 << 10
const MaxTimeoutMS = 60000

type Engine interface {
	Create(context.Context, *pb.EngineOptions) (contract.Info, error)
	Destroy(context.Context) error
	Info(context.Context) (contract.Info, error)
	CheckConfig(context.Context, string) error
	Start(context.Context, string) error
	Reload(context.Context, string) error
	Stop(context.Context) error
	Status(context.Context) (contract.Status, error)
	Groups(context.Context) ([]contract.Group, error)
	Outbounds(context.Context) ([]contract.Outbound, error)
	SelectOutbound(context.Context, string, string) error
	URLTest(context.Context, string, string, int32) (int32, error)
	SetMode(context.Context, string) error
	SetPaused(context.Context, bool) error
	CloseConnections(context.Context) error
	NextEvent(context.Context, int32) (*contract.Event, error)
	UpdateNetwork(context.Context, contract.Network) error
	MemoryPressure(context.Context, int32) error
}

func errorMessage(err error) *pb.Error {
	value := contract.ErrorValue(err)
	if value == nil {
		return nil
	}
	return &pb.Error{Code: strings.ToValidUTF8(value.Code, "\uFFFD"), Message: strings.ToValidUTF8(value.Message, "\uFFFD")}
}
func encode(response *pb.Response) []byte {
	if proto.Size(response) > MaxMessageBytes {
		return Failure(response.Id, contract.Fail("response_too_large", "control response exceeds the size limit"))
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(response)
	if err != nil || len(data) > MaxMessageBytes {
		data, _ = proto.Marshal(&pb.Response{ProtocolVersion: Version, Id: response.Id, Result: &pb.Response_Error{Error: &pb.Error{Code: "internal_error", Message: "could not encode control response"}}})
	}
	return data
}
func Failure(id uint64, err error) []byte {
	return encode(&pb.Response{ProtocolVersion: Version, Id: id, Result: &pb.Response_Error{Error: errorMessage(err)}})
}
func FailureFor(request []byte, err error) []byte {
	var decoded pb.Request
	if len(request) <= MaxMessageBytes {
		_ = (proto.UnmarshalOptions{RecursionLimit: 64}).Unmarshal(request, &decoded)
	}
	return Failure(decoded.Id, err)
}
func Handle(parent context.Context, engine Engine, payload []byte) []byte {
	if len(payload) == 0 || len(payload) > MaxMessageBytes {
		return Failure(0, contract.Fail("invalid_argument", "control request size is out of range"))
	}
	var request pb.Request
	if err := (proto.UnmarshalOptions{RecursionLimit: 64}).Unmarshal(payload, &request); err != nil {
		return Failure(0, contract.Fail("invalid_argument", "malformed protobuf request"))
	}
	if request.ProtocolVersion != Version {
		return Failure(request.Id, contract.Fail("unsupported_version", "protocol_version must be 1"))
	}
	if request.TimeoutMs > MaxTimeoutMS {
		return Failure(request.Id, contract.Fail("invalid_argument", "timeout_ms exceeds 60000"))
	}
	timeout := request.TimeoutMs
	if timeout == 0 {
		timeout = 30000
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	if request.DeadlineUnixMs > 0 {
		absolute := time.UnixMilli(request.DeadlineUnixMs)
		if absolute.Before(deadline) {
			deadline = absolute
		}
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Failure(request.Id, contract.Wrap("canceled", err))
	}
	response := &pb.Response{ProtocolVersion: Version, Id: request.Id, Result: &pb.Response_Empty{Empty: &pb.Empty{}}}
	var err error
	switch method := request.Method.(type) {
	case *pb.Request_CreateEngine:
		var info contract.Info
		info, err = engine.Create(ctx, method.CreateEngine)
		response.Result = &pb.Response_Created{Created: convertInfo(info)}
	case *pb.Request_DestroyEngine:
		err = engine.Destroy(ctx)
	case *pb.Request_GetInfo:
		var info contract.Info
		info, err = engine.Info(ctx)
		response.Result = &pb.Response_Info{Info: convertInfo(info)}
	case *pb.Request_CheckConfig:
		err = engine.CheckConfig(ctx, method.CheckConfig.GetConfigJson())
	case *pb.Request_Start:
		err = engine.Start(ctx, method.Start.GetConfigJson())
	case *pb.Request_Reload:
		err = engine.Reload(ctx, method.Reload.GetConfigJson())
	case *pb.Request_Stop:
		err = engine.Stop(ctx)
	case *pb.Request_GetStatus:
		var status contract.Status
		status, err = engine.Status(ctx)
		var failure *pb.Error
		if status.LastError != nil {
			failure = errorMessage(status.LastError)
		}
		response.Result = &pb.Response_Status{Status: &pb.Status{State: status.State, Generation: status.Generation, StartedAtUnixMs: status.StartedAt, DroppedEvents: status.DroppedEvents, LastError: failure, UploadBytes: status.UploadBytes, DownloadBytes: status.DownloadBytes, Connections: uint64(max(0, status.Connections)), Mode: status.Mode, Modes: status.Modes, Paused: status.Paused, GoMemoryLimitBytes: status.GoMemoryLimitBytes, GoHeapAllocBytes: status.GoHeapAllocBytes}}
	case *pb.Request_ListGroups:
		var values []contract.Group
		values, err = engine.Groups(ctx)
		groups := &pb.Groups{}
		for _, g := range values {
			groups.Groups = append(groups.Groups, &pb.Group{Tag: g.Tag, Type: g.Type, Selected: g.Selected, Selectable: g.Selectable, Outbounds: convertOutbounds(g.Outbounds)})
		}
		response.Result = &pb.Response_Groups{Groups: groups}
	case *pb.Request_ListOutbounds:
		var values []contract.Outbound
		values, err = engine.Outbounds(ctx)
		response.Result = &pb.Response_Outbounds{Outbounds: &pb.Outbounds{Outbounds: convertOutbounds(values)}}
	case *pb.Request_SelectOutbound:
		err = engine.SelectOutbound(ctx, method.SelectOutbound.GetGroup(), method.SelectOutbound.GetOutbound())
	case *pb.Request_UrlTest:
		limit := method.UrlTest.GetTimeoutMs()
		if limit > MaxTimeoutMS {
			err = contract.Fail("invalid_argument", "URL test timeout exceeds 60000")
			break
		}
		if limit == 0 {
			limit = timeout
		}
		var delay int32
		delay, err = engine.URLTest(ctx, method.UrlTest.GetOutbound(), method.UrlTest.GetUrl(), int32(limit))
		response.Result = &pb.Response_Delay{Delay: &pb.Delay{DelayMs: delay}}
	case *pb.Request_SetMode:
		err = engine.SetMode(ctx, method.SetMode.GetMode())
	case *pb.Request_SetPaused:
		err = engine.SetPaused(ctx, method.SetPaused.GetPaused())
	case *pb.Request_CloseConnections:
		err = engine.CloseConnections(ctx)
	case *pb.Request_NextEvent:
		limit := method.NextEvent.GetTimeoutMs()
		if limit > MaxTimeoutMS {
			err = contract.Fail("invalid_argument", "event timeout exceeds 60000")
			break
		}
		var event *contract.Event
		event, err = engine.NextEvent(ctx, int32(limit))
		if err == nil && event != nil {
			var converted *pb.Event
			converted, err = convertEvent(event)
			response.Result = &pb.Response_Event{Event: converted}
		}
	case *pb.Request_UpdateNetwork:
		value := method.UpdateNetwork
		if value == nil {
			err = contract.Fail("invalid_argument", "missing network snapshot")
			break
		}
		network := contract.Network{DefaultInterface: int(value.DefaultInterface), WIFISSID: value.WifiSsid, WIFIBSSID: value.WifiBssid}
		for _, i := range value.Interfaces {
			if i == nil {
				err = contract.Fail("invalid_argument", "nil interface")
				break
			}
			network.Interfaces = append(network.Interfaces, contract.Interface{Index: int(i.Index), Name: i.Name, MTU: int(i.Mtu), Addresses: i.Addresses, DNSServers: i.DnsServers, DNSSearchDomains: i.DnsSearchDomains, Type: i.Type, Metered: i.Metered, Constrained: i.Constrained, Loopback: i.Loopback})
		}
		if err == nil {
			err = engine.UpdateNetwork(ctx, network)
		}
	case *pb.Request_MemoryPressure:
		err = engine.MemoryPressure(ctx, int32(method.MemoryPressure.GetLevel()))
	default:
		err = contract.Fail("unknown_method", "unknown or missing protobuf method")
	}
	if err != nil {
		if ctx.Err() != nil {
			err = contract.Wrap("canceled", ctx.Err())
		}
		return Failure(request.Id, err)
	}
	return encode(response)
}
func convertInfo(info contract.Info) *pb.Info {
	return &pb.Info{ApiVersion: uint32(info.APIVersion), SdkVersion: info.SDKVersion, CoreVersion: info.CoreVersion, BuildTags: info.BuildTags, Goos: info.OS, Goarch: info.Architecture, Capabilities: info.Capabilities}
}
func convertOutbounds(values []contract.Outbound) []*pb.Outbound {
	result := make([]*pb.Outbound, 0, len(values))
	for _, v := range values {
		result = append(result, &pb.Outbound{Tag: v.Tag, Type: v.Type, DelayMs: v.DelayMS})
	}
	return result
}
func convertEvent(event *contract.Event) (*pb.Event, error) {
	result := &pb.Event{Sequence: event.Sequence, TimeUnixMs: event.Time}
	switch value := event.Data.(type) {
	case contract.Log:
		result.Data = &pb.Event_Log{Log: &pb.Log{Level: value.Level, Message: value.Message, Truncated: value.Truncated}}
	case contract.StateChange:
		change := &pb.StateChange{State: value.State}
		if value.Error != nil {
			change.Error = errorMessage(value.Error)
		}
		result.Data = &pb.Event_State{State: change}
	case contract.GroupHealth:
		result.Data = &pb.Event_GroupHealth{GroupHealth: &pb.GroupHealth{Group: value.Group, Healthy: value.Healthy, Unhealthy: value.Unhealthy, Fallback: value.Fallback}}
	default:
		return nil, contract.Fail("internal_error", "unsupported internal event type")
	}
	return result, nil
}
