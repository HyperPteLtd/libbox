package memory

import (
	"math"
	"testing"
)

func TestProfilesAndLeaseRestoration(t *testing.T) {
	original := Policy{Limit: math.MaxInt64, GCPercent: 100}
	current := original
	m := manager{read: func() Policy { return current }, apply: func(value Policy) { current = value }}
	profile, err := Resolve("auto", 0, 0, true, "ios")
	if err != nil || profile.Limit != 40<<20 || profile.GCPercent != 50 {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	first := m.acquire(profile)
	second := m.acquire(&Policy{Limit: 20 << 20, GCPercent: 25})
	if current.Limit != 20<<20 || current.GCPercent != 25 {
		t.Fatal(current)
	}
	first.Close()
	if current.Limit != 20<<20 {
		t.Fatal("released wrong process limit")
	}
	second.Close()
	second.Close()
	if current != original {
		t.Fatalf("not restored: %+v", current)
	}
	if value, err := Resolve("auto", 0, 0, true, "darwin"); err != nil || value != nil {
		t.Fatal("desktop default should not change runtime")
	}
	if lease := m.acquire(nil); lease != nil {
		t.Fatal("default lease should be empty")
	}
}
func TestHostLimitAndDisabledGC(t *testing.T) {
	current := Policy{Limit: 16 << 20, GCPercent: -1}
	m := manager{read: func() Policy { return current }, apply: func(p Policy) { current = p }}
	lease := m.acquire(&Policy{Limit: 40 << 20, GCPercent: 50})
	if current.Limit != 16<<20 || current.GCPercent != 50 {
		t.Fatal(current)
	}
	lease.Close()
	if current.GCPercent != -1 || current.Limit != 16<<20 {
		t.Fatal(current)
	}
}
func TestInvalidProfiles(t *testing.T) {
	for _, test := range []struct {
		profile string
		limit   int64
		gc      int
	}{{"invalid", 0, 0}, {"default", -1, 0}, {"default", 1024, 0}, {"default", 0, -1}, {"default", 0, 1001}} {
		if _, err := Resolve(test.profile, test.limit, test.gc, false, "windows"); err == nil {
			t.Fatal(test)
		}
	}
}
