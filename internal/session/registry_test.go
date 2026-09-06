package session_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"order_of_things/internal/golden"
	"order_of_things/internal/session"
)

// play resumes a session. Registry sessions are created paused, so that a viewer
// pressing Play sees the tournament from its first event rather than joining one
// already in progress.
func play(handle *session.Handle) *session.Handle {
	handle.Session.Resume()
	return handle
}

func registry(t *testing.T, games int) (*session.Registry, *golden.Store) {
	t.Helper()
	store, err := golden.Open("")
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	r := session.NewRegistry(store)
	r.SetGames(games)
	// Tests run flat out; the one-event-per-second default is for a viewer.
	r.SetInterval(0)
	t.Cleanup(r.StopAll)
	return r, store
}

func TestRegistryCreatesAndTracksSessions(t *testing.T) {
	r, _ := registry(t, 20)
	ctx := context.Background()

	handle, err := r.Create(ctx, 42)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if handle.Seed != 42 || handle.Games != 20 {
		t.Errorf("handle = %+v, want seed 42 over 20 games", handle)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}

	found, ok := r.Get(handle.Id)
	if !ok {
		t.Fatalf("Get(%q) found nothing", handle.Id)
	}
	if found.Session != handle.Session {
		t.Error("Get returned a different session")
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get returned a session for an unknown id")
	}
}

// A session's seed is all that is needed to reproduce it, so handing someone your
// seed hands them your exact tournament.
func TestRegistryReproducesASeed(t *testing.T) {
	r, _ := registry(t, 20)
	ctx := context.Background()

	first, err := r.Create(ctx, 1234)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	firstResult := play(first).Session.Wait()

	second, err := r.Create(ctx, 1234)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	secondResult := play(second).Session.Wait()

	if first.Id == second.Id {
		t.Error("two sessions were given the same id")
	}
	if secondResult.StateHash != firstResult.StateHash {
		t.Errorf("replaying seed 1234 gave state root %016x, want %016x",
			secondResult.StateHash, firstResult.StateHash)
	}
}

// Everyone plays the same tournament. The seed is fixed on purpose: what this
// system demonstrates is that the outcome holds while the viewer is breaking it,
// not that a seeded generator repeats itself.
func TestSessionsPlayTheCanonicalTournament(t *testing.T) {
	r, _ := registry(t, 10)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		handle, err := r.Create(ctx, 0)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if handle.Seed != session.CanonicalSeed {
			t.Fatalf("session %d drew seed %d, want the canonical %d",
				i, handle.Seed, session.CanonicalSeed)
		}
	}
}

// Prepare makes the canonical chain available before any session exists, so a
// replica restarted in the first seconds of the first session still has
// something to be checked against.
func TestPrepareGeneratesTheCanonicalChainUpFront(t *testing.T) {
	r, store := registry(t, 10)

	if store.Validator(session.CanonicalSeed, 10) != nil {
		t.Fatal("a chain existed before Prepare")
	}
	if err := r.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	validator := store.Validator(session.CanonicalSeed, 10)
	if validator == nil {
		t.Fatal("no canonical chain after Prepare")
	}
	recorded, _ := store.Get(session.CanonicalSeed, 10)
	if want := 10 * 3; len(recorded.Chain) != want {
		t.Errorf("chain has %d roots, want %d", len(recorded.Chain), want)
	}
}

// Every session gets a canonical chain, whatever seed it drew, because creating
// one generates the reference first. Without that, only pre-chosen seeds could
// validate a restarting replica.
func TestRegistryGeneratesAReferenceForEverySession(t *testing.T) {
	r, store := registry(t, 20)
	ctx := context.Background()

	handle, err := r.Create(ctx, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	recorded, ok := store.Get(handle.Seed, 20)
	if !ok {
		t.Fatalf("no reference recorded for seed %d", handle.Seed)
	}
	if want := 20 * 3; len(recorded.Chain) != want {
		t.Errorf("chain has %d roots, want %d (one per event)", len(recorded.Chain), want)
	}
	if store.Validator(handle.Seed, 20) == nil {
		t.Error("no validator for a session that was just created")
	}

	// And the live session reproduces it.
	result := play(handle).Session.Wait()
	if result.StateHash != recorded.StateHash {
		t.Errorf("live session state root %016x, reference %016x", result.StateHash, recorded.StateHash)
	}
}

// The reference is generated once per seed and reused, not regenerated per
// session.
func TestRegistryReusesAnExistingReference(t *testing.T) {
	r, store := registry(t, 20)
	ctx := context.Background()

	if _, err := r.Create(ctx, 777); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, _ := store.Get(777, 20)

	if _, err := r.Create(ctx, 777); err != nil {
		t.Fatalf("create: %v", err)
	}
	second, _ := store.Get(777, 20)

	if first.StateHash != second.StateHash {
		t.Error("the reference changed between sessions on the same seed")
	}
}

func TestRegistryStopEndsASession(t *testing.T) {
	r, _ := registry(t, 100)
	ctx := context.Background()

	handle, err := r.Create(ctx, 42)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.Stop(handle.Id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("Len = %d after stopping the only session", r.Len())
	}
	if _, ok := r.Get(handle.Id); ok {
		t.Error("a stopped session is still reachable")
	}
	if err := r.Stop(handle.Id); err == nil {
		t.Error("stopping an unknown session succeeded")
	}
}

func TestRegistryResultIsAvailableOnlyWhenFinished(t *testing.T) {
	r, _ := registry(t, 20)
	ctx := context.Background()

	handle, err := r.Create(ctx, 42)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := r.Result("nope"); ok {
		t.Error("Result returned something for an unknown id")
	}

	result := play(handle).Session.Wait()
	waitFor(t, "the registry to record the result", func() bool {
		_, ok := r.Result(handle.Id)
		return ok
	})
	got, _ := r.Result(handle.Id)
	if got.StateHash != result.StateHash {
		t.Errorf("registry result %016x, session reported %016x", got.StateHash, result.StateHash)
	}
}

func TestRegistryListsSessionsInCreationOrder(t *testing.T) {
	r, _ := registry(t, 10)
	ctx := context.Background()

	created := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		handle, err := r.Create(ctx, int64(i+1))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		created = append(created, handle.Id)
	}
	ids := r.Ids()
	if len(ids) != len(created) {
		t.Fatalf("Ids returned %d, want %d", len(ids), len(created))
	}
	for i := range ids {
		if ids[i] != created[i] {
			t.Fatalf("Ids[%d] = %q, want %q (ordering is not stable)", i, ids[i], created[i])
		}
	}
}

// A viewer who closes the tab leaves a session running at one event per second
// forever, so idle sessions have to be reclaimed.
func TestRegistryEvictsIdleAndOverAgeSessions(t *testing.T) {
	r, _ := registry(t, 100)
	ctx := context.Background()

	idle, err := r.Create(ctx, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	active, err := r.Create(ctx, 2)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Nothing is idle yet.
	if stopped := r.Evict(time.Hour, 0); len(stopped) != 0 {
		t.Errorf("evicted %v with a one-hour idle window", stopped)
	}

	// Touching one keeps it; the other is reclaimed.
	time.Sleep(20 * time.Millisecond)
	r.Get(active.Id)

	stopped := r.Evict(15*time.Millisecond, 0)
	if len(stopped) != 1 || stopped[0] != idle.Id {
		t.Fatalf("evicted %v, want just the idle session %q", stopped, idle.Id)
	}
	if _, ok := r.Get(idle.Id); ok {
		t.Error("the evicted session is still reachable")
	}
	if _, ok := r.Get(active.Id); !ok {
		t.Error("the touched session was evicted")
	}

	// A hard age limit reclaims even a session someone is still watching.
	if stopped := r.Evict(time.Hour, time.Nanosecond); len(stopped) != 1 {
		t.Errorf("max-age eviction stopped %v, want the remaining session", stopped)
	}
	if r.Len() != 0 {
		t.Errorf("Len = %d after evicting everything", r.Len())
	}
}

func TestRegistryStopAllEndsEverything(t *testing.T) {
	r, _ := registry(t, 100)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := r.Create(ctx, int64(i+1)); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	r.StopAll()
	if r.Len() != 0 {
		t.Errorf("Len = %d after StopAll", r.Len())
	}
	r.StopAll() // idempotent
}

// A session outlives the request that created it: cancelling the caller's context
// must not kill a running tournament.
func TestSessionOutlivesTheCreatingContext(t *testing.T) {
	r, _ := registry(t, 20)

	ctx, cancel := context.WithCancel(context.Background())
	handle, err := r.Create(ctx, 42)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cancel()

	result := play(handle).Session.Wait()
	if result.Games != 20 {
		t.Errorf("completed %d of 20 games after the creating context was cancelled", result.Games)
	}
}

// Sessions are fully independent, so a server can run many at once without them
// affecting each other.
func TestRegistryRunsSessionsConcurrently(t *testing.T) {
	r, _ := registry(t, 25)
	ctx := context.Background()

	handles := make([]*session.Handle, 0, 8)
	for i := 0; i < 8; i++ {
		handle, err := r.Create(ctx, int64(i%3)+1) // three distinct tournaments
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		handles = append(handles, handle)
	}

	roots := make(map[int64]uint64)
	for _, handle := range handles {
		result := play(handle).Session.Wait()
		if result.Games != 25 {
			t.Errorf("session %s completed %d of 25 games", handle.Id, result.Games)
		}
		if seen, ok := roots[handle.Seed]; ok && seen != result.StateHash {
			t.Errorf("seed %d produced two different state roots: %016x and %016x",
				handle.Seed, seen, result.StateHash)
		}
		roots[handle.Seed] = result.StateHash
	}
	if len(roots) != 3 {
		t.Errorf("saw %d distinct seeds, want 3", len(roots))
	}
}

// References survive the process, so a server restart does not lose the ability
// to validate a replica against a seed it has already seen.
func TestReferencesPersistAcrossRegistries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golden.json")
	ctx := context.Background()

	store, err := golden.Open(path)
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	first := session.NewRegistry(store)
	first.SetGames(20)
	first.SetInterval(0)
	handle, err := first.Create(ctx, 4242)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	play(handle).Session.Wait()
	first.StopAll()

	reopened, err := golden.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Validator(4242, 20) == nil {
		t.Fatal("the reference did not survive being written and reread")
	}

	second := session.NewRegistry(reopened)
	second.SetGames(20)
	second.SetInterval(0)
	defer second.StopAll()
	revived, err := second.Create(ctx, 4242)
	if err != nil {
		t.Fatalf("create in a fresh registry: %v", err)
	}
	if got := play(revived).Session.Wait(); len(got.Quarantined) != 0 {
		t.Errorf("a clean session failed the persisted reference: %v", got.Quarantined)
	}
}
