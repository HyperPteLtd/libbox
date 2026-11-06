package protocol

import (
	"encoding/hex"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"testing"
)

func TestStableMethodNumbersAndWireVector(t *testing.T) {
	request := (&Request{}).ProtoReflect().Descriptor().Fields()
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{"protocol_version": 1, "id": 2, "timeout_ms": 3, "deadline_unix_ms": 4, "get_info": 10, "check_config": 11, "start": 12, "reload": 13, "stop": 14, "get_status": 15, "list_groups": 16, "list_outbounds": 17, "select_outbound": 18, "url_test": 19, "set_mode": 20, "set_paused": 21, "close_connections": 22, "next_event": 23, "update_network": 24, "memory_pressure": 25, "create_engine": 26, "destroy_engine": 27} {
		if field := request.ByName(name); field == nil || field.Number() != number {
			t.Fatalf("field %s number changed", name)
		}
	}
	data, err := proto.Marshal(&Request{ProtocolVersion: 1, Id: 42, Method: &Request_GetStatus{GetStatus: &Empty{}}})
	if err != nil || hex.EncodeToString(data) != "0801102a7a00" {
		t.Fatalf("wire vector changed: %x %v", data, err)
	}
}

func TestStableEventNumbers(t *testing.T) {
	event := (&Event{}).ProtoReflect().Descriptor().Fields()
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{"sequence": 1, "time_unix_ms": 2, "log": 3, "state": 4, "group_health": 5} {
		if field := event.ByName(name); field == nil || field.Number() != number {
			t.Fatalf("event field %s number changed", name)
		}
	}
	health := (&GroupHealth{}).ProtoReflect().Descriptor().Fields()
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{"group": 1, "healthy": 2, "unhealthy": 3, "fallback": 4} {
		if field := health.ByName(name); field == nil || field.Number() != number {
			t.Fatalf("group health field %s number changed", name)
		}
	}
	data, err := proto.Marshal(&Event{Sequence: 1, Data: &Event_GroupHealth{GroupHealth: &GroupHealth{Group: "JP", Unhealthy: []string{"a"}, Fallback: "HK"}}})
	if err != nil || hex.EncodeToString(data) != "08012a0b0a024a501a01612202484b" {
		t.Fatalf("wire vector changed: %x %v", data, err)
	}
}
