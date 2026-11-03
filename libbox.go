// Package libbox embeds sing-box through versioned protobuf MessageSessions.
// All core-specific types and behavior are isolated in internal/singbox.
package libbox

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HyperPteLtd/libbox/internal/backend"
	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/HyperPteLtd/libbox/internal/memory"
	"github.com/HyperPteLtd/libbox/internal/syncutil"
)

type session struct {
	core   backend.Runtime
	ctx    context.Context
	cancel context.CancelFunc
	config string
	calls  sync.WaitGroup
}

// engine owns one runtime. Lifecycle changes are serialized. Queries are safe
// concurrently; Stop/Close cancel in-flight URL tests before releasing the core.
// The message session owns this private runtime; it is not an FFI surface.
type engine struct {
	op          syncutil.Mutex
	networkOp   syncutil.Mutex
	mu          sync.RWMutex
	factory     backend.Factory
	options     contract.Options
	hooks       contract.Hooks
	network     contract.Network
	run         *session
	building    *session
	pending     context.CancelFunc
	stopEpoch   atomic.Uint64
	state       string
	closed      bool
	closing     bool
	generation  uint64
	startedAt   int64
	lastError   error
	events      *eventQueue
	memoryLease *memory.Lease
}

func (e *engine) initialized() error {
	if e == nil || e.factory == nil || e.events == nil {
		return contract.Fail("uninitialized", "engine is uninitialized")
	}
	return nil
}

func newEngineOptions(options contract.Options, factory backend.Factory) (*engine, error) {
	if !filepath.IsAbs(options.WorkingDirectory) {
		return nil, contract.Fail("invalid_argument", "working_directory must be absolute")
	}
	if options.TempDirectory == "" {
		options.TempDirectory = filepath.Join(options.WorkingDirectory, "tmp")
	}
	if !filepath.IsAbs(options.TempDirectory) {
		return nil, contract.Fail("invalid_argument", "temp_directory must be absolute")
	}
	if options.NetworkMode != "native" && options.NetworkMode != "host" {
		return nil, contract.Fail("invalid_argument", "network_mode must be native or host")
	}
	if options.EventBuffer < 1 || options.EventBuffer > 4096 {
		return nil, contract.Fail("invalid_argument", "event_buffer must be in [1,4096]")
	}
	policy, err := memory.Resolve(options.MemoryProfile, options.MemoryLimitBytes, options.GCPercent, options.UnderNetworkExtension, runtime.GOOS)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{options.WorkingDirectory, options.TempDirectory} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, contract.Wrap("io_error", err)
		}
	}
	return &engine{factory: factory, options: options, state: "stopped", network: contract.Network{DefaultInterface: -1}, events: newEventQueue(options.EventBuffer), memoryLease: memory.Acquire(policy)}, nil
}

// CheckConfig constructs and closes a validation instance without starting it.
// Core configuration is native sing-box JSON, versioned with the pinned core.
func (e *engine) CheckConfig(configJSON string) error {
	return e.checkConfigContext(context.Background(), configJSON)
}
func (e *engine) checkConfigContext(ctx context.Context, configJSON string) error {
	if err := e.initialized(); err != nil {
		return err
	}
	if err := e.op.LockContext(ctx); err != nil {
		return contract.Wrap("canceled", err)
	}
	defer e.op.Unlock()
	e.mu.RLock()
	closed := e.closed || e.closing
	e.mu.RUnlock()
	if closed {
		return contract.Fail("closed", "engine is closed")
	}
	return contract.Wrap("invalid_config", e.factory.Check(ctx, configJSON, e.options))
}

func (e *engine) changeState(state string, err error) {
	e.mu.Lock()
	e.state, e.lastError = state, err
	e.mu.Unlock()
	e.events.emit("state", contract.StateChange{State: state, Error: contract.ErrorValue(err)})
}

// Start fails if a runtime is already running. Use Reload to replace it.
func (e *engine) Start(configJSON string) error {
	return e.startContext(context.Background(), configJSON)
}
func (e *engine) startContext(ctx context.Context, configJSON string) error {
	if err := e.initialized(); err != nil {
		return err
	}
	epoch := e.stopEpoch.Load()
	if err := e.op.LockContext(ctx); err != nil {
		return contract.Wrap("canceled", err)
	}
	defer e.op.Unlock()
	e.mu.RLock()
	closed, running := e.closed || e.closing, e.run != nil
	e.mu.RUnlock()
	if closed {
		return contract.Fail("closed", "engine is closed")
	}
	if running {
		return contract.Fail("invalid_state", "engine is already running")
	}
	e.changeState("starting", nil)
	if err := e.start(configJSON, epoch, ctx); err != nil {
		e.changeState("stopped", err)
		return err
	}
	e.changeState("running", nil)
	return nil
}

// start is called with op held, never with mu held (host callbacks may query state).
func (e *engine) start(config string, epoch uint64, requestContext context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	stopRequestCancellation := context.AfterFunc(requestContext, cancel)
	defer stopRequestCancellation()
	e.mu.Lock()
	e.pending = cancel
	if e.stopEpoch.Load() != epoch {
		cancel()
	}
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.pending = nil; e.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return contract.Wrap("canceled", err)
	}
	e.mu.RLock()
	network := e.network
	e.mu.RUnlock()
	core, err := e.factory.Build(ctx, config, e.options, e.hooks, network, e.events.emit)
	var candidate *session
	if err == nil {
		candidate = &session{core: core, ctx: ctx, cancel: cancel, config: config}
		e.networkOp.Lock()
		e.mu.Lock()
		e.building = candidate
		network = e.network
		e.mu.Unlock()
		if e.options.NetworkMode == "host" {
			err = core.UpdateNetwork(network)
		}
		e.networkOp.Unlock()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = core.Start()
	}
	if err == nil {
		err = ctx.Err()
	}
	// Detach the request deadline before publishing a successful, long-lived
	// runtime. A later caller timeout must not silently stop a running tunnel.
	stopRequestCancellation()
	if err == nil {
		err = requestContext.Err()
	}
	var actualMode string
	if err == nil {
		actualMode = core.Status().Mode
	}
	e.mu.Lock()
	if err == nil && (e.closing || e.closed || e.stopEpoch.Load() != epoch) {
		err = context.Canceled
	}
	e.building = nil
	if err == nil {
		e.run = candidate
		e.options.ModeOverride = actualMode
		e.generation++
		e.startedAt = time.Now().UnixMilli()
	}
	e.mu.Unlock()
	if err != nil {
		cancel()
		if candidate != nil {
			candidate.calls.Wait()
		}
		if core != nil {
			if cleanupErr := core.Close(); cleanupErr != nil {
				value := contract.ErrorValue(contract.Wrap("start_failed", err))
				err = contract.Fail(value.Code, value.Message+"; cleanup: "+cleanupErr.Error())
			}
		}
		return contract.Wrap("start_failed", err)
	}
	return nil
}

// Reload validates before stopping the old runtime. Startup failure attempts to
// restart the old config; errors distinguish restored and failed rollback.
// Existing connections are interrupted; this is not a seamless reload.
func (e *engine) Reload(configJSON string) error {
	return e.reloadContext(context.Background(), configJSON)
}
func (e *engine) reloadContext(ctx context.Context, configJSON string) error {
	if err := e.initialized(); err != nil {
		return err
	}
	epoch := e.stopEpoch.Load()
	if err := e.op.LockContext(ctx); err != nil {
		return contract.Wrap("canceled", err)
	}
	defer e.op.Unlock()
	e.mu.RLock()
	closed, old := e.closed || e.closing, e.run
	e.mu.RUnlock()
	if closed {
		return contract.Fail("closed", "engine is closed")
	}
	if old == nil {
		return contract.Fail("not_running", "engine is not running")
	}
	if err := e.factory.Check(ctx, configJSON, e.options); err != nil {
		return contract.Wrap("invalid_config", err)
	}
	if err := ctx.Err(); err != nil {
		return contract.Wrap("canceled", err)
	}
	return e.replaceRuntime(ctx, configJSON, epoch, old, e.options.ModeOverride)
}

// Called with op held. A mode change uses the same rollback/stop fencing as reload.
func (e *engine) replaceRuntime(ctx context.Context, configJSON string, epoch uint64, old *session, rollbackMode string) error {
	e.changeState("reloading", nil)
	if err := e.stop(); err != nil {
		e.options.ModeOverride = rollbackMode
		e.changeState("stopped", err)
		return err
	}
	if err := e.start(configJSON, epoch, ctx); err != nil {
		e.options.ModeOverride = rollbackMode
		if e.stopEpoch.Load() != epoch || ctx.Err() != nil {
			failure := contract.Fail("canceled", "reload interrupted by stop or close")
			e.changeState("stopped", failure)
			return failure
		}
		if rollbackErr := e.start(old.config, epoch, ctx); rollbackErr != nil {
			failure := contract.Fail("rollback_failed", fmt.Sprintf("new configuration: %v; restore previous configuration: %v", err, rollbackErr))
			e.changeState("stopped", failure)
			return failure
		}
		failure := contract.Fail("reload_failed", fmt.Sprintf("previous configuration restored; new configuration: %v", err))
		e.changeState("running", failure)
		return failure
	}
	e.changeState("running", nil)
	return nil
}

func (e *engine) interrupt() {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.pending != nil {
		e.pending()
	}
	if e.run != nil {
		e.run.cancel()
	}
}
func (e *engine) stop() error {
	e.interrupt()
	e.mu.Lock()
	old := e.run
	e.run = nil
	e.startedAt = 0
	e.mu.Unlock()
	if old == nil {
		return nil
	}
	// No new call can acquire old after it is detached under mu. Wait without
	// holding mu, so host callbacks can still read SDK status during shutdown.
	old.calls.Wait()
	return contract.Wrap("stop_failed", old.core.Close())
}

// Stop is idempotent and leaves the engine reusable.
func (e *engine) Stop() error {
	return e.stopContext(context.Background())
}
func (e *engine) stopContext(ctx context.Context) error {
	if err := e.initialized(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return contract.Wrap("canceled", err)
	}
	e.stopEpoch.Add(1)
	e.interrupt()
	// Cancellation above accepted a stop intent. Once the runtime is canceled,
	// complete cleanup even when the caller's admission budget expires.
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.RLock()
	closed, running := e.closed, e.run != nil
	e.mu.RUnlock()
	if closed {
		return nil
	}
	if running {
		e.changeState("stopping", nil)
	}
	err := e.stop()
	e.changeState("stopped", err)
	return err
}

// Close stops the runtime and ends the event stream. It is idempotent.
func (e *engine) Close() error {
	if err := e.initialized(); err != nil {
		return err
	}
	e.mu.Lock()
	e.closing = true
	e.mu.Unlock()
	e.stopEpoch.Add(1)
	e.interrupt()
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.RLock()
	closed, running := e.closed, e.run != nil
	e.mu.RUnlock()
	if closed {
		return nil
	}
	if running {
		e.changeState("stopping", nil)
	}
	err := e.stop()
	e.mu.Lock()
	e.closed = true
	e.state = "closed"
	e.lastError = err
	e.hooks = contract.Hooks{}
	e.network = contract.Network{DefaultInterface: -1}
	e.mu.Unlock()
	e.events.close()
	e.memoryLease.Close()
	return err
}

func (e *engine) withRuntime(fn func(*session) error) error {
	if err := e.initialized(); err != nil {
		return err
	}
	e.mu.RLock()
	if e.closed || e.closing {
		e.mu.RUnlock()
		return contract.Fail("closed", "engine is closed")
	}
	if e.run == nil {
		e.mu.RUnlock()
		return contract.Fail("not_running", "engine is not running")
	}
	current := e.run
	current.calls.Add(1)
	e.mu.RUnlock()
	defer current.calls.Done()
	return contract.Wrap("core_error", fn(current))
}

func (e *engine) statusValue() (contract.Status, error) {
	if err := e.initialized(); err != nil {
		return contract.Status{}, err
	}
	e.mu.RLock()
	status := contract.Status{APIVersion: contract.APIVersion, State: e.state, Generation: e.generation, StartedAt: e.startedAt, LastError: contract.ErrorValue(e.lastError), DroppedEvents: e.events.droppedCount(), RuntimeStatus: contract.RuntimeStatus{Modes: []string{}}}
	status.GoMemoryLimitBytes, status.GoHeapAllocBytes = memory.Snapshot()
	current := e.run
	if current != nil {
		current.calls.Add(1)
	}
	e.mu.RUnlock()
	if current != nil {
		defer current.calls.Done()
		status.RuntimeStatus = current.core.Status()
	}
	return status, nil
}

func (e *engine) outboundValues() (values []contract.Outbound, err error) {
	err = e.withRuntime(func(s *session) error { values = s.core.Outbounds(); return nil })
	return
}

func (e *engine) groupValues() (values []contract.Group, err error) {
	err = e.withRuntime(func(s *session) error { values = s.core.Groups(); return nil })
	return
}
func (e *engine) SelectOutbound(groupTag, outboundTag string) error {
	return e.withRuntime(func(s *session) error { return s.core.SelectOutbound(groupTag, outboundTag) })
}

// URLTest measures one outbound using an HTTP(S) HEAD request. Stop cancels it.
func (e *engine) URLTest(outboundTag, testURL string, timeoutMS int32) (delayMS int32, err error) {
	return e.urlTestContext(context.Background(), outboundTag, testURL, timeoutMS)
}
func (e *engine) urlTestContext(requestContext context.Context, outboundTag, testURL string, timeoutMS int32) (delayMS int32, err error) {
	if timeoutMS < 1 || timeoutMS > 60000 {
		return 0, contract.Fail("invalid_argument", "timeoutMS must be in [1,60000]")
	}
	parsed, parseErr := url.Parse(testURL)
	if parseErr != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return 0, contract.Fail("invalid_argument", "testURL must be an absolute HTTP(S) URL")
	}
	err = e.withRuntime(func(s *session) error {
		ctx, cancel := context.WithTimeout(s.ctx, time.Duration(timeoutMS)*time.Millisecond)
		defer cancel()
		stop := context.AfterFunc(requestContext, cancel)
		defer stop()
		if requestContext.Err() != nil {
			cancel()
		}
		var testErr error
		delayMS, testErr = s.core.URLTest(ctx, outboundTag, testURL)
		if ctx.Err() != nil {
			return contract.Wrap("canceled", ctx.Err())
		}
		return testErr
	})
	return
}
func (e *engine) SetMode(mode string) error {
	return e.setModeContext(context.Background(), mode)
}
func (e *engine) setModeContext(ctx context.Context, mode string) error {
	if err := e.initialized(); err != nil {
		return err
	}
	epoch := e.stopEpoch.Load()
	if err := e.op.LockContext(ctx); err != nil {
		return contract.Wrap("canceled", err)
	}
	defer e.op.Unlock()
	e.mu.RLock()
	old, closed := e.run, e.closed || e.closing
	e.mu.RUnlock()
	if closed {
		return contract.Fail("closed", "engine is closed")
	}
	if old == nil {
		return contract.Fail("not_running", "engine is not running")
	}
	status := old.core.Status()
	var canonical string
	for _, candidate := range status.Modes {
		if strings.EqualFold(candidate, mode) {
			canonical = candidate
			break
		}
	}
	if canonical == "" {
		return contract.Fail("invalid_argument", "mode is not present in this configuration")
	}
	if strings.EqualFold(status.Mode, canonical) {
		return nil
	}
	previous := e.options.ModeOverride
	e.options.ModeOverride = canonical
	return e.replaceRuntime(ctx, old.config, epoch, old, previous)
}
func (e *engine) CloseConnections() error {
	return e.withRuntime(func(s *session) error { return s.core.CloseConnections() })
}
func (e *engine) SetPaused(paused bool) error {
	return e.withRuntime(func(s *session) error { return s.core.SetPaused(paused) })
}

func (e *engine) updateNetworkValue(ctx context.Context, network contract.Network) error {
	if err := e.initialized(); err != nil {
		return err
	}
	if err := network.Validate(); err != nil {
		return err
	}
	if err := e.networkOp.LockContext(ctx); err != nil {
		return contract.Wrap("canceled", err)
	}
	defer e.networkOp.Unlock()
	e.mu.Lock()
	closed, current := e.closed || e.closing, e.run
	if current == nil {
		current = e.building
	}
	if !closed && e.options.NetworkMode == "host" {
		e.network = network
		if current != nil {
			current.calls.Add(1)
		}
	}
	e.mu.Unlock()
	if closed {
		return contract.Fail("closed", "engine is closed")
	}
	if e.options.NetworkMode != "host" {
		return contract.Fail("unsupported", "network snapshots require network_mode=host")
	}
	if current != nil {
		defer current.calls.Done()
		if err := current.core.UpdateNetwork(network); err != nil {
			return contract.Wrap("core_error", err)
		}
	}
	return nil
}
