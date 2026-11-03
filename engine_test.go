package libbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HyperPteLtd/libbox/internal/backend"
	"github.com/HyperPteLtd/libbox/internal/contract"
)

type fakeFactory struct {
	check func(string) error
	build func(context.Context, string) (backend.Runtime, error)
}

func testOptions(t *testing.T) contract.Options {
	t.Helper()
	return contract.Options{WorkingDirectory: t.TempDir(), NetworkMode: "native", EventBuffer: 256}
}
func statusOf(t *testing.T, e *engine) contract.Status {
	t.Helper()
	value, err := e.statusValue()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBoundedEventsAndCloseWakeup(t *testing.T) {
	q := newEventQueue(2)
	for range 4 {
		q.emit("log", contract.Log{Level: "info", Message: "test"})
	}
	if q.droppedCount() != 2 {
		t.Fatal("missing drop count")
	}
	for _, sequence := range []uint64{3, 4} {
		event, err := q.nextValue(context.Background(), 0)
		if err != nil || event == nil || event.Sequence != sequence {
			t.Fatalf("event=%+v err=%v", event, err)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := q.nextValue(context.Background(), 60000); done <- err }()
	q.close()
	select {
	case err := <-done:
		requireCode(t, err, "closed")
	case <-time.After(time.Second):
		t.Fatal("blocked event reader")
	}
}

func (f fakeFactory) Check(_ context.Context, config string, _ contract.Options) error {
	if f.check != nil {
		return f.check(config)
	}
	return nil
}
func (f fakeFactory) Build(ctx context.Context, config string, _ contract.Options, _ contract.Hooks, _ contract.Network, _ contract.Emit) (backend.Runtime, error) {
	return f.build(ctx, config)
}

type fakeRuntime struct {
	start         func() error
	closed        bool
	urlTest       func(context.Context) (int32, error)
	updateNetwork func(contract.Network) error
}

func (r *fakeRuntime) Start() error {
	if r.start != nil {
		return r.start()
	}
	return nil
}
func (r *fakeRuntime) Close() error                                            { r.closed = true; return nil }
func (*fakeRuntime) Status() contract.RuntimeStatus                            { return contract.RuntimeStatus{Modes: []string{}} }
func (*fakeRuntime) Outbounds() []contract.Outbound                            { return []contract.Outbound{} }
func (*fakeRuntime) Groups() []contract.Group                                  { return []contract.Group{} }
func (*fakeRuntime) SelectOutbound(string, string) error                       { return nil }
func (r *fakeRuntime) URLTest(ctx context.Context, _, _ string) (int32, error) { return r.urlTest(ctx) }
func (*fakeRuntime) SetMode(string) error                                      { return nil }
func (*fakeRuntime) CloseConnections() error                                   { return nil }
func (*fakeRuntime) SetPaused(bool) error                                      { return nil }
func (r *fakeRuntime) UpdateNetwork(n contract.Network) error {
	if r.updateNetwork != nil {
		return r.updateNetwork(n)
	}
	return nil
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || contract.ErrorValue(err).Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestReloadValidationPreservesRuntime(t *testing.T) {
	old := &fakeRuntime{}
	e, err := newEngineOptions(testOptions(t), fakeFactory{check: func(string) error { return errors.New("bad config") }, build: func(context.Context, string) (backend.Runtime, error) { return old, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Start("old"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, e.Reload("invalid"), "invalid_config")
	if old.closed {
		t.Fatal("validation failure closed old runtime")
	}
	if status := statusOf(t, e); status.State != "running" || status.Generation != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestReloadRestoresOldConfigAndCleansFailedRuntime(t *testing.T) {
	var built []string
	failed := &fakeRuntime{start: func() error { return errors.New("address in use") }}
	old := &fakeRuntime{}
	e, err := newEngineOptions(testOptions(t), fakeFactory{build: func(_ context.Context, config string) (backend.Runtime, error) {
		built = append(built, config)
		if config == "bad" {
			return failed, nil
		}
		if len(built) == 1 {
			return old, nil
		}
		return &fakeRuntime{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Start("good"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, e.Reload("bad"), "reload_failed")
	if !old.closed || !failed.closed {
		t.Fatal("old/failed runtime leaked")
	}
	if len(built) != 3 || built[2] != "good" {
		t.Fatalf("not restored: %v", built)
	}
	status := statusOf(t, e)
	if status.State != "running" || status.Generation != 2 || status.LastError.Code != "reload_failed" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestRollbackFailureLeavesEngineStopped(t *testing.T) {
	builds := 0
	e, err := newEngineOptions(testOptions(t), fakeFactory{build: func(context.Context, string) (backend.Runtime, error) {
		builds++
		if builds == 1 {
			return &fakeRuntime{}, nil
		}
		return nil, errors.New("cannot build")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Start("old"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, e.Reload("new"), "rollback_failed")
	if status := statusOf(t, e); status.State != "stopped" || status.StartedAt != 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
	_, err = e.groupValues()
	requireCode(t, err, "not_running")
}

func TestStopCancelsInFlightURLTest(t *testing.T) {
	entered := make(chan struct{})
	r := &fakeRuntime{urlTest: func(ctx context.Context) (int32, error) { close(entered); <-ctx.Done(); return 0, ctx.Err() }}
	e, err := newEngineOptions(testOptions(t), fakeFactory{build: func(context.Context, string) (backend.Runtime, error) { return r, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Start("ok"); err != nil {
		t.Fatal(err)
	}
	testDone := make(chan error, 1)
	go func() { _, err := e.URLTest("direct", "http://localhost/", 60000); testDone <- err }()
	<-entered
	stopDone := make(chan error, 1)
	go func() { stopDone <- e.Stop() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop deadlocked behind URL test")
	}
	requireCode(t, <-testDone, "canceled")
	if !r.closed {
		t.Fatal("runtime not closed")
	}
}

func TestStatusFromInFlightCallDuringStop(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	var e *engine
	r := &fakeRuntime{urlTest: func(ctx context.Context) (int32, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		// A host callback can read status without holding shutdown hostage.
		_, err := e.statusValue()
		if err != nil {
			return 0, err
		}
		return 0, ctx.Err()
	}}
	var err error
	e, err = newEngineOptions(testOptions(t), fakeFactory{build: func(context.Context, string) (backend.Runtime, error) { return r, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Start("ok"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.URLTest("direct", "http://localhost/", 60000); done <- err }()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop() }()
	<-canceled
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status reentry deadlocked shutdown")
	}
	requireCode(t, <-done, "canceled")
}

func TestStopDuringReloadDoesNotRestartOldConfig(t *testing.T) {
	entered := make(chan struct{})
	var built []string
	e, err := newEngineOptions(testOptions(t), fakeFactory{build: func(ctx context.Context, config string) (backend.Runtime, error) {
		built = append(built, config)
		if config == "new" {
			return &fakeRuntime{start: func() error { close(entered); <-ctx.Done(); return ctx.Err() }}, nil
		}
		return &fakeRuntime{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Start("old"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Reload("new") }()
	<-entered
	if err = e.Stop(); err != nil {
		t.Fatal(err)
	}
	requireCode(t, <-done, "canceled")
	if len(built) != 2 {
		t.Fatalf("unexpected rollback after stop: %v", built)
	}
}

func TestConcurrentEventProducers(t *testing.T) {
	q := newEventQueue(16)
	var producers sync.WaitGroup
	for range 4 {
		producers.Go(func() {
			for range 100 {
				q.emit("test", nil)
			}
		})
	}
	producers.Wait()
	if q.droppedCount() != 384 {
		t.Fatalf("unexpected dropped count %d", q.droppedCount())
	}
}
