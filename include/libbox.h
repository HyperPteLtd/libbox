#ifndef HYPER_LIBBOX_H
#define HYPER_LIBBOX_H
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif

/* Protobuf transport ABI v1. All logical operations (including create/destroy)
 * use Request/Response from proto/libbox/v1/control.proto. No JSON entry points.
 *
 * A callback receives HostRequest and writes HostResponse into the provided
 * buffer (capacity 65536). Return byte count, or -1 for a bridge failure.
 * Inputs/outputs are borrowed for the call only. The callback may run on Go
 * threads; do not reenter lifecycle operations. Keep callback/context alive
 * until session_free returns. Native descriptors are process-local. */
typedef int64_t (*libbox_host_message_handler)(void *context, const void *request,
    uint64_t request_length, void *response, uint64_t response_capacity);

/* Allocates only an opaque transport endpoint. Pass NULL for native networking.
 * Initialize the engine with create_engine. Never mix handles from different
 * library instances. */
uint64_t libbox_session_new(void *context, libbox_host_message_handler callback);

/* Raw protobuf, not NUL-terminated and not stream-framed. Maximum request/result
 * size is 4 MiB. response_length must be non-NULL. The returned allocation belongs
 * to libbox and must be released exactly once using libbox_free_buffer. */
void *libbox_session_call(uint64_t session, void *request, uint64_t request_length,
                         uint64_t *response_length);
void libbox_free_buffer(void *value);

/* Transport cleanup waits for core shutdown and in-flight host callbacks.
 * Send destroy_engine first when the host needs a teardown error response. */
void libbox_session_free(uint64_t session);

#ifdef __cplusplus
}
#endif
#endif
