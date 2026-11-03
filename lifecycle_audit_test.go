package libbox

import (
	"context"
	"github.com/HyperPteLtd/libbox/internal/backend"
	"github.com/HyperPteLtd/libbox/internal/contract"
	pb "github.com/HyperPteLtd/libbox/protocol/v1"
	"sync"
	"testing"
	"time"
)

func TestAcceptedStopOutlivesAdmissionDeadline(t *testing.T) {
	var lifetime context.Context
	r := &fakeRuntime{}
	e, err := newEngineOptions(testOptions(t), fakeFactory{build: func(ctx context.Context, _ string) (backend.Runtime, error) { lifetime = ctx; return r, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Start("ok"); err != nil {
		t.Fatal(err)
	}
	e.op.Lock()
	var released sync.Once
	release := func() { released.Do(e.op.Unlock) }
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.stopContext(ctx) }()
	<-lifetime.Done()
	<-ctx.Done()
	select {
	case err := <-done:
		t.Fatalf("stop abandoned cleanup: %v", err)
	default:
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not complete")
	}
	if !r.closed || statusOf(t, e).State != "stopped" {
		t.Fatal("canceled runtime left published")
	}
}
func TestNetworkUpdateReachesStartingRuntime(t *testing.T) {
	entered, updated := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r := &fakeRuntime{start: func() error {
		close(entered)
		select {
		case <-updated:
			return nil
		case <-time.After(time.Second):
			return context.DeadlineExceeded
		}
	}, updateNetwork: func(n contract.Network) error {
		if n.DefaultInterface == 2 {
			once.Do(func() { close(updated) })
		}
		return nil
	}}
	opts := testOptions(t)
	opts.NetworkMode = "host"
	e, err := newEngineOptions(opts, fakeFactory{build: func(context.Context, string) (backend.Runtime, error) { return r, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	done := make(chan error, 1)
	go func() { done <- e.Start("ok") }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = e.updateNetworkValue(ctx, contract.Network{DefaultInterface: 2, Interfaces: []contract.Interface{{Index: 2, Name: "test", MTU: 1500}}})
	if err != nil {
		t.Fatal("network update blocked behind startup:", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestSessionCloseRejectsNewWorkWhileCallbackDrains(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var life context.Context
	r := &fakeRuntime{urlTest: func(ctx context.Context) (int32, error) { close(entered); <-ctx.Done(); <-release; return 0, ctx.Err() }}
	e, err := newEngineOptions(testOptions(t), fakeFactory{build: func(ctx context.Context, _ string) (backend.Runtime, error) { life = ctx; return r, nil }})
	if err != nil {
		t.Fatal(err)
	}
	s := NewMessageSession(nil)
	s.engine = e
	if err = e.Start("ok"); err != nil {
		t.Fatal(err)
	}
	testDone := make(chan error, 1)
	go func() { _, err := e.URLTest("test", "http://localhost/", 5000); testDone <- err }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	<-life.Done()
	response := sessionCall(t, s, &pb.Request{Method: &pb.Request_GetInfo{GetInfo: &pb.Empty{}}})
	if response.GetError().GetCode() != "closed" {
		close(release)
		t.Fatal(response)
	}
	select {
	case <-closed:
		close(release)
		t.Fatal("close returned before callback exited")
	default:
	}
	close(release)
	<-testDone
	if err = <-closed; err != nil {
		t.Fatal(err)
	}
}
