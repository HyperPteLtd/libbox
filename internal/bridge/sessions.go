package bridge

import (
	libbox "github.com/HyperPteLtd/libbox"
	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/HyperPteLtd/libbox/internal/control"
	"sync"
)

type Sessions struct {
	mu     sync.RWMutex
	next   uint64
	values map[uint64]*libbox.MessageSession
}

func (s *Sessions) New(handler libbox.HostMessageHandler) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[uint64]*libbox.MessageSession{}
	}
	s.next++
	s.values[s.next] = libbox.NewMessageSession(handler)
	return s.next
}
func (s *Sessions) Call(handle uint64, request []byte) []byte {
	s.mu.RLock()
	value := s.values[handle]
	s.mu.RUnlock()
	if value == nil {
		return control.FailureFor(request, contract.Fail("invalid_handle", "unknown message session"))
	}
	return value.HandleMessage(request)
}
func (s *Sessions) Free(handle uint64) {
	s.mu.RLock()
	value := s.values[handle]
	s.mu.RUnlock()
	if value != nil {
		// Keep the closing session reachable until callbacks have drained. A
		// concurrent duplicate free must not let its caller release the native
		// callback context while the first free is still using it.
		_ = value.Close()
		s.mu.Lock()
		delete(s.values, handle)
		s.mu.Unlock()
	}
}
