// Package memory manages the Go runtime's PROCESS-WIDE settings. Multiple SDK
// engines cooperate by choosing the strictest active lease and restoring the
// host's original settings when the last lease is released.
package memory

import (
	"math"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"

	"github.com/HyperPteLtd/libbox/internal/contract"
)

type Policy struct {
	Limit     int64
	GCPercent int
}
type manager struct {
	mu       sync.Mutex
	next     uint64
	policies map[uint64]Policy
	base     Policy
	read     func() Policy
	apply    func(Policy)
}
type Lease struct {
	once    sync.Once
	release func()
}

func (l *Lease) Close() {
	if l != nil {
		l.once.Do(l.release)
	}
}

func current() Policy {
	samples := []metrics.Sample{{Name: "/gc/gogc:percent"}}
	metrics.Read(samples)
	gc := int(samples[0].Value.Uint64())
	return Policy{Limit: debug.SetMemoryLimit(-1), GCPercent: gc}
}

var process = manager{read: current, apply: func(p Policy) { debug.SetMemoryLimit(p.Limit); debug.SetGCPercent(p.GCPercent) }}

func Resolve(profile string, limit int64, gc int, underNE bool, goos string) (*Policy, error) {
	if profile == "" || profile == "auto" {
		profile = "default"
		if underNE && goos == "ios" {
			profile = "network_extension"
		}
	}
	if profile != "default" && profile != "network_extension" {
		return nil, contract.Fail("invalid_argument", "unknown memory_profile")
	}
	if limit < 0 || gc < 0 || gc > 1000 {
		return nil, contract.Fail("invalid_argument", "memory limit must be nonnegative; gc_percent must be in [0,1000]")
	}
	if limit > 0 && limit < 8<<20 {
		return nil, contract.Fail("invalid_argument", "memory_limit_bytes must be at least 8 MiB")
	}
	if profile == "network_extension" {
		if limit == 0 {
			limit = 40 << 20
		}
		if gc == 0 {
			gc = 50
		}
	}
	if limit == 0 && gc == 0 {
		return nil, nil
	}
	return &Policy{Limit: limit, GCPercent: gc}, nil
}
func Acquire(policy *Policy) *Lease { return process.acquire(policy) }
func (m *manager) acquire(policy *Policy) *Lease {
	if policy == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.policies) == 0 {
		m.base = m.read()
		m.policies = map[uint64]Policy{}
	}
	m.next++
	id := m.next
	m.policies[id] = *policy
	m.recompute()
	return &Lease{release: func() { m.mu.Lock(); defer m.mu.Unlock(); delete(m.policies, id); m.recompute() }}
}
func (m *manager) recompute() {
	if len(m.policies) == 0 {
		m.apply(m.base)
		return
	}
	result := m.base
	// An existing host memory limit also remains an upper bound. A disabled GC
	// baseline (-1) can be enabled by an explicit SDK lease and is restored later.
	if result.GCPercent < 0 {
		result.GCPercent = math.MaxInt
	}
	for _, p := range m.policies {
		if p.Limit > 0 && p.Limit < result.Limit {
			result.Limit = p.Limit
		}
		if p.GCPercent > 0 && p.GCPercent < result.GCPercent {
			result.GCPercent = p.GCPercent
		}
	}
	if result.GCPercent == math.MaxInt {
		result.GCPercent = -1
	}
	m.apply(result)
}
func Pressure(level int32) error {
	switch level {
	case 0:
		return nil
	case 1:
		runtime.GC()
	case 2:
		debug.FreeOSMemory()
	default:
		return contract.Fail("invalid_argument", "memory pressure must be 0, 1, or 2")
	}
	return nil
}
func Snapshot() (limit int64, heap uint64) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return debug.SetMemoryLimit(-1), stats.HeapAlloc
}
