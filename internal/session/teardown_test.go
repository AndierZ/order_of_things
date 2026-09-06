package session

import (
	"context"
	"testing"
	"time"
)

// A session that will not stop must not be allowed to wedge the mechanism that
// reclaims sessions. Blocking on it forever turns one stuck session into a stuck
// server: the reclaim loop never ticks again and nothing is ever reclaimed after
// the first hang.
//
// This is a white-box test because the failure it guards against is only
// reachable by constructing an entry that never signals -- which is exactly what
// a hung component goroutine looks like from here.
func TestStopGivesUpOnASessionThatWillNotStop(t *testing.T) {
	r := NewRegistry(nil)
	r.SetStopTimeout(50 * time.Millisecond)

	_, cancel := context.WithCancel(context.Background())
	stuck := &entry{
		id:     "wedged",
		cancel: cancel,
		done:   make(chan struct{}), // never closed
	}

	start := time.Now()
	if r.stop(stuck) {
		t.Error("stop claimed a session stopped when it never did")
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("stop waited %v on a session that never stops", elapsed)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("stop gave up after %v, before its own timeout", elapsed)
	}
}

// And the reclaim path survives one: a wedged session must not prevent the
// others from being reclaimed, now or ever again.
func TestOneWedgedSessionDoesNotStopTheReclaimer(t *testing.T) {
	r := NewRegistry(nil)
	r.SetStopTimeout(30 * time.Millisecond)

	_, cancel := context.WithCancel(context.Background())
	healthy := make(chan struct{})
	close(healthy)

	r.mu.Lock()
	r.sessions["wedged"] = &entry{id: "wedged", cancel: cancel, done: make(chan struct{})}
	r.sessions["fine"] = &entry{id: "fine", cancel: func() {}, done: healthy}
	r.mu.Unlock()

	start := time.Now()
	stopped := r.Evict(0, 0)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Evict took %v; one wedged session blocked the sweep", elapsed)
	}
	if len(stopped) != 2 {
		t.Errorf("reclaimed %v, want both -- a session that will not stop is still out of the registry", stopped)
	}
	if r.Len() != 0 {
		t.Errorf("Len = %d after reclaiming everything", r.Len())
	}

	// And the registry keeps working afterwards.
	if _, ok := r.Get("wedged"); ok {
		t.Error("a wedged session is still reachable after being abandoned")
	}
}
