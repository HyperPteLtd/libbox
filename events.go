package libbox

import (
	"context"
	"github.com/HyperPteLtd/libbox/internal/contract"
	"sync"
	"time"
)

type eventQueue struct {
	mu       sync.Mutex
	queue    chan contract.Event
	done     chan struct{}
	closed   bool
	sequence uint64
	dropped  uint64
}

func newEventQueue(size int) *eventQueue {
	return &eventQueue{queue: make(chan contract.Event, size), done: make(chan struct{})}
}
func (q *eventQueue) emit(kind string, data any) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.sequence++
	event := contract.Event{APIVersion: contract.APIVersion, Sequence: q.sequence, Time: time.Now().UnixMilli(), Type: kind, Data: data}
	select {
	case q.queue <- event:
		return
	default:
	}
	select {
	case <-q.queue:
		q.dropped++
	default:
	}
	q.queue <- event
}
func (q *eventQueue) droppedCount() uint64 { q.mu.Lock(); defer q.mu.Unlock(); return q.dropped }
func (q *eventQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.done)
	for {
		select {
		case <-q.queue:
		default:
			return
		}
	}
}

func (q *eventQueue) nextValue(ctx context.Context, timeoutMS int32) (*contract.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Wrap("canceled", err)
	}
	if timeoutMS < 0 || timeoutMS > 60000 {
		return nil, contract.Fail("invalid_argument", "timeoutMS must be in [0,60000]")
	}
	select {
	case <-q.done:
		return nil, contract.Fail("closed", "engine is closed")
	default:
	}
	if timeoutMS == 0 {
		select {
		case event := <-q.queue:
			return &event, nil
		default:
			return nil, nil
		}
	}
	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case event := <-q.queue:
		return &event, nil
	case <-q.done:
		return nil, contract.Fail("closed", "engine is closed")
	case <-timer.C:
		return nil, nil
	case <-ctx.Done():
		return nil, contract.Wrap("canceled", ctx.Err())
	}
}
