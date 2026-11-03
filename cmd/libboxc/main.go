//go:build cgo

package main

/*
#include <stdlib.h>
#include "../../include/libbox.h"
static int64_t call_host_message(libbox_host_message_handler f, void *ctx,
    const void *request, uint64_t length, void *response, uint64_t capacity) {
    return f(ctx, request, length, response, capacity);
}
*/
import "C"

import (
	libbox "github.com/HyperPteLtd/libbox"
	"github.com/HyperPteLtd/libbox/internal/bridge"
	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/HyperPteLtd/libbox/internal/control"
	"unsafe"
)

var sessions bridge.Sessions

func main() {}

type hostMessageCallback struct {
	context unsafe.Pointer
	fn      C.libbox_host_message_handler
}

func (c hostMessageCallback) HandleHostMessage(request []byte) ([]byte, error) {
	input := C.CBytes(request)
	defer C.free(input)
	output := C.malloc(control.MaxHostResponseBytes)
	defer C.free(output)
	length := C.call_host_message(c.fn, c.context, input, C.uint64_t(len(request)), output, control.MaxHostResponseBytes)
	if length < 0 || length > control.MaxHostResponseBytes {
		return nil, contract.Fail("host_error", "invalid host callback length")
	}
	return C.GoBytes(output, C.int(length)), nil
}

//export libbox_session_new
func libbox_session_new(context unsafe.Pointer, callback C.libbox_host_message_handler) C.uint64_t {
	var handler libbox.HostMessageHandler
	if callback != nil {
		handler = hostMessageCallback{context, callback}
	}
	return C.uint64_t(sessions.New(handler))
}

//export libbox_session_call
func libbox_session_call(handle C.uint64_t, request unsafe.Pointer, length C.uint64_t, responseLength *C.uint64_t) unsafe.Pointer {
	if responseLength == nil {
		return nil
	}
	var result []byte
	if length > control.MaxMessageBytes || (request == nil && length != 0) {
		result = control.Failure(0, contract.Fail("invalid_argument", "invalid protobuf buffer"))
	} else {
		result = sessions.Call(uint64(handle), C.GoBytes(request, C.int(length)))
	}
	*responseLength = C.uint64_t(len(result))
	return C.CBytes(result)
}

//export libbox_free_buffer
func libbox_free_buffer(value unsafe.Pointer) { C.free(value) }

//export libbox_session_free
func libbox_session_free(handle C.uint64_t) { sessions.Free(uint64(handle)) }
