package libbox

import (
	"context"
	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/HyperPteLtd/libbox/internal/memory"
)

// NotifyMemoryPressure requests a process-wide Go GC (1) or GC plus page release
// (2). Zero does nothing. It does not kill the process or drop active connections.
func (e *engine) NotifyMemoryPressure(level int32) error {
	if err := e.initialized(); err != nil {
		return err
	}
	e.mu.RLock()
	closed := e.closed
	e.mu.RUnlock()
	if closed {
		return contract.Fail("closed", "engine is closed")
	}
	return memory.Pressure(level)
}

type engineControl struct{ e *engine }

func (c engineControl) CheckConfig(ctx context.Context, value string) error {
	return c.e.checkConfigContext(ctx, value)
}
func (c engineControl) Start(ctx context.Context, value string) error {
	return c.e.startContext(ctx, value)
}
func (c engineControl) Reload(ctx context.Context, value string) error {
	return c.e.reloadContext(ctx, value)
}
func (c engineControl) Stop(ctx context.Context) error { return c.e.stopContext(ctx) }
func (c engineControl) Status(ctx context.Context) (contract.Status, error) {
	if err := ctx.Err(); err != nil {
		return contract.Status{}, err
	}
	return c.e.statusValue()
}
func (c engineControl) Groups(ctx context.Context) ([]contract.Group, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.e.groupValues()
}
func (c engineControl) Outbounds(ctx context.Context) ([]contract.Outbound, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.e.outboundValues()
}
func (c engineControl) SelectOutbound(ctx context.Context, group, outbound string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.e.SelectOutbound(group, outbound)
}
func (c engineControl) URLTest(ctx context.Context, outbound, url string, timeout int32) (int32, error) {
	return c.e.urlTestContext(ctx, outbound, url, timeout)
}
func (c engineControl) SetMode(ctx context.Context, value string) error {
	return c.e.setModeContext(ctx, value)
}
func (c engineControl) SetPaused(ctx context.Context, value bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.e.SetPaused(value)
}
func (c engineControl) CloseConnections(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.e.CloseConnections()
}
func (c engineControl) NextEvent(ctx context.Context, timeout int32) (*contract.Event, error) {
	return c.e.events.nextValue(ctx, timeout)
}
func (c engineControl) UpdateNetwork(ctx context.Context, value contract.Network) error {
	return c.e.updateNetworkValue(ctx, value)
}
func (c engineControl) MemoryPressure(ctx context.Context, level int32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.e.NotifyMemoryPressure(level)
}
