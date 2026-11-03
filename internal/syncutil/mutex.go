// Package syncutil provides cancellation-aware admission to serialized operations.
package syncutil

import (
	"context"
	"sync"
)

type Mutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *Mutex) initialize() { m.once.Do(func() { m.token = make(chan struct{}, 1) }) }
func (m *Mutex) Lock()       { _ = m.LockContext(context.Background()) }
func (m *Mutex) LockContext(ctx context.Context) error {
	m.initialize()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-m.token
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Mutex) Unlock() { <-m.token }
