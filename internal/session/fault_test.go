package session_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"order_of_things/internal/fsm"
	"order_of_things/internal/golden"
	"order_of_things/internal/session"
)

func status(t *testing.T, s *session.Session, component, replica string) session.ReplicaStatus {
	t.Helper()
	for _, st := range s.Status() {
		if st.Component == component && st.Replica == replica {
			return st
		}
	}
	t.Fatalf("no replica %s/%s", component, replica)
	return session.ReplicaStatus{}
}

// Availability (invariant 5) is a liveness property, verified by killing
// something and checking the system still makes progress -- not by comparing
// outputs. Half of every pair is removed mid-tournament and it still finishes,
// with the same outcome a healthy run produces.
func TestTournamentSurvivesLosingHalfOfEveryPair(t *testing.T) {
	reference, _ := run(t, session.Config{Seed: 42, Games: 60, Replicas: 2})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := session.New(session.Config{Seed: 42, Games: 60, Replicas: 2})
	s.Start(ctx)

	// Wait for the tournament to be genuinely underway before pulling anything.
	waitForGames(t, s, 5)
	for _, component := range append([]string{"game-injector"}, strategyNames()...) {
		if err := s.Kill(component, "r1"); err != nil {
			t.Fatalf("killing %s/r1: %v", component, err)
		}
		if st := status(t, s, component, "r1"); st.Running || !st.Killed {
			t.Errorf("%s/r1 after kill: running=%v killed=%v", component, st.Running, st.Killed)
		}
	}

	result := s.Wait()
	if result.Games != 60 {
		t.Fatalf("completed %d of 60 games after losing half of every pair", result.Games)
	}
	if result.StateHash != reference.StateHash {
		t.Errorf("state root %016x after failover, want %016x", result.StateHash, reference.StateHash)
	}
	if len(result.Quarantined) != 0 {
		t.Errorf("killing a replica quarantined something: %v", result.Quarantined)
	}
}

// A killed replica comes back as a fresh instance with empty state, replays the
// log from the sequencer, and rejoins its pair. Nothing about the outcome changes.
func TestKilledReplicaReplaysAndRejoins(t *testing.T) {
	reference, _ := run(t, session.Config{Seed: 42, Games: 60, Replicas: 2})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := session.New(session.Config{Seed: 42, Games: 60, Replicas: 2})
	s.Start(ctx)

	waitForGames(t, s, 5)
	if err := s.Kill("retaliator", "r1"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitForGames(t, s, 15)
	if err := s.Restart("retaliator", "r1"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if st := status(t, s, "retaliator", "r1"); !st.Running || st.Quarantined {
		t.Errorf("after restart: running=%v quarantined=%v", st.Running, st.Quarantined)
	}

	result := s.Wait()
	if result.Games != 60 {
		t.Fatalf("completed %d of 60 games", result.Games)
	}
	if result.StateHash != reference.StateHash {
		t.Errorf("state root %016x after replay and rejoin, want %016x", result.StateHash, reference.StateHash)
	}
	if len(result.Quarantined) != 0 {
		t.Errorf("a healthy replica was quarantined on rejoin: %v", result.Quarantined)
	}
}

// A replica whose decisions are not a pure function of the event history
// disagrees with its sibling, and the pair notices. Exactly one half is
// quarantined -- and deliberately not necessarily the buggy one, because
// divergence detection proves only that they disagreed.
func TestImpureReplicaIsQuarantined(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := session.New(session.Config{
		Seed: 42, Games: 60, Replicas: 2,
		Bug: &session.Bug{Component: "flipper", Replica: "r1",
			Defect: session.Defect{ImpureClock: true}},
	})
	result := s.Run(ctx)

	if result.Games != 60 {
		t.Fatalf("completed %d of 60 games; the surviving replica should carry it", result.Games)
	}
	if len(result.Quarantined) != 1 {
		t.Fatalf("quarantined %v, want exactly one half of the flipper pair", result.Quarantined)
	}
	if got := result.Quarantined[0]; got != "flipper/r0" && got != "flipper/r1" {
		t.Errorf("quarantined %q, want one of the flipper replicas", got)
	}
	// Every other pair is untouched.
	for _, st := range s.Status() {
		if st.Component != "flipper" && st.Quarantined {
			t.Errorf("%s/%s was quarantined by an unrelated replica's bug", st.Component, st.Replica)
		}
	}
}

// Quarantine has to be visible the moment it happens, without anyone poking the
// supervisor. A replica quarantines itself on its own goroutine; if nothing is
// waiting on that goroutine, the supervisor keeps reporting it healthy until it
// joins for some unrelated reason -- and then looks like whatever triggered the
// join is what caused the quarantine.
func TestSelfQuarantineIsVisibleWithoutBeingPoked(t *testing.T) {
	s, _ := started(t, session.Config{Seed: 42, Games: 80, Replicas: 2})

	waitFor(t, "the tournament to get going", func() bool {
		return s.Tracker().Snapshot().Completed >= 10
	})
	if err := s.Kill("flipper", "r1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tournament to move on", func() bool {
		return s.Tracker().Snapshot().Completed >= 20
	})
	if err := s.RestartWithBug("flipper", "r1", session.Defect{ImpureClock: true}); err != nil {
		t.Fatal(err)
	}

	// No Kill, no Wait, no other call that would join the goroutine: the status
	// has to arrive on its own.
	waitFor(t, "the defective replica to report itself quarantined", func() bool {
		got := status(t, s, "flipper", "r1")
		return got.Quarantined && !got.Running
	})
	if got := status(t, s, "flipper", "r1"); got.Killed {
		t.Error("a replica that quarantined itself is reported as killed")
	}
}

// Quarantine refuses a divergent instance, not the name forever. An operator
// redeploying is entitled to try again -- and the fresh instance has to earn its
// place by replaying correctly, like any other.
func TestQuarantinedReplicaCanBeRedeployedClean(t *testing.T) {
	s, _ := started(t, session.Config{Seed: 42, Games: 80, Replicas: 2, Reference: reference(t, 42, 80)})

	waitFor(t, "the tournament to get going", func() bool {
		return s.Tracker().Snapshot().Completed >= 10
	})
	if err := s.Kill("flipper", "r1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tournament to move on", func() bool {
		return s.Tracker().Snapshot().Completed >= 20
	})
	if err := s.RestartWithBug("flipper", "r1", session.Defect{CorruptPayoff: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the defective replica to be refused", func() bool {
		return status(t, s, "flipper", "r1").Quarantined
	})

	// Redeploy it clean. It replays, passes, and rejoins.
	if err := s.Restart("flipper", "r1"); err != nil {
		t.Fatalf("a quarantined replica could not be redeployed: %v", err)
	}
	got := status(t, s, "flipper", "r1")
	if got.Quarantined || got.Killed || !got.Running {
		t.Fatalf("after a clean redeploy: %+v, want running and clear", got)
	}

	result := s.Wait()
	if result.Games != 80 {
		t.Errorf("completed %d of 80 games", result.Games)
	}
	if len(result.Quarantined) != 0 {
		t.Errorf("the redeployed replica did not survive: %v", result.Quarantined)
	}
}

// A restart is a redeploy of that replica: it stops whatever is running and
// brings back a fresh instance. Making the operator kill it first is bookkeeping
// the supervisor is better placed to do.
func TestRestartingARunningReplicaRedeploysIt(t *testing.T) {
	s, _ := started(t, session.Config{Seed: 42, Games: 80, Replicas: 2})

	waitFor(t, "the tournament to get going", func() bool {
		return s.Tracker().Snapshot().Completed >= 10
	})

	// No kill first, in either case.
	if err := s.Restart("flipper", "r1"); err != nil {
		t.Fatalf("restarting a live replica: %v", err)
	}
	if got := status(t, s, "flipper", "r1"); !got.Running || got.Killed || got.Quarantined {
		t.Errorf("after a clean redeploy: %+v, want running and clear", got)
	}

	if err := s.RestartWithBug("flipper", "r1", session.Defect{ImpureClock: true}); err != nil {
		t.Fatalf("bugging a live replica: %v", err)
	}
	waitFor(t, "the defective replica to be refused", func() bool {
		return status(t, s, "flipper", "r1").Quarantined
	})

	result := s.Wait()
	if result.Games != 80 {
		t.Errorf("completed %d of 80 games", result.Games)
	}
}

// The edge case worth naming: lose one half to a kill and the other to a defect,
// and the player has nobody left to answer for it. The tournament comes to rest
// rather than doing anything clever, and stays there until a replica returns.
// This is the most direct demonstration of why there are two of everything.
func TestKillingOneHalfAndBuggingTheOtherStallsTheTournament(t *testing.T) {
	s, _ := started(t, session.Config{
		Seed: 42, Games: 80, Replicas: 2, Reference: reference(t, 42, 80),
	})

	waitFor(t, "the tournament to get going", func() bool {
		return s.Tracker().Snapshot().Completed >= 8
	})
	if err := s.Kill("flipper", "r0"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := s.RestartWithBug("flipper", "r1", session.Defect{ImpureClock: true}); err != nil {
		t.Fatalf("bug: %v", err)
	}

	waitFor(t, "both halves of the flipper pair to be down", func() bool {
		r0, r1 := status(t, s, "flipper", "r0"), status(t, s, "flipper", "r1")
		return !r0.Live() && !r1.Live()
	})
	if got := status(t, s, "flipper", "r0"); !got.Killed {
		t.Errorf("flipper/r0 = %+v, want killed", got)
	}
	if got := status(t, s, "flipper", "r1"); !got.Quarantined {
		t.Errorf("flipper/r1 = %+v, want quarantined", got)
	}

	// The tournament runs on until it needs flipper, then comes to rest.
	waitFor(t, "the tournament to come to rest on flipper", func() bool {
		stalled, on := s.Stalled()
		return stalled && on == "flipper" && restsAt(s, 20*time.Millisecond)
	})
	held := s.Tracker().Snapshot().Version
	if !restsAt(s, 200*time.Millisecond) {
		t.Errorf("advanced from version %d while stalled with no live flipper", held)
	}
	// Nothing else was harmed by it.
	for _, st := range s.Status() {
		if st.Component != "flipper" && (st.Quarantined || st.Killed) {
			t.Errorf("%s/%s went down with the flipper pair", st.Component, st.Replica)
		}
	}

	// Bringing either half back clean releases it.
	if err := s.Restart("flipper", "r0"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	waitFor(t, "the tournament to recover", func() bool {
		stalled, _ := s.Stalled()
		return !stalled && s.Tracker().Snapshot().Version > held
	})

	result := s.Wait()
	if result.Games != 80 {
		t.Errorf("completed %d of 80 games after recovering", result.Games)
	}
}

// The tracker is a read model, not one half of an arbitrated pair, so it is not
// a fault-injection target.
func TestTrackerIsNotFaultInjectable(t *testing.T) {
	s := session.New(session.Config{Seed: 42, Games: 5, Replicas: 2})
	if err := s.Kill("game-tracker", "r0"); err == nil {
		t.Error("killing the tracker was allowed")
	}
	if err := s.Kill("flipper", "nope"); err == nil {
		t.Error("killing an unknown replica was allowed")
	}
}

// Golden persistence closes the loop: a session in a fresh process has no live
// sibling to check itself against, so it checks itself against the durable record
// for its seed.
func TestGoldenOutcomeRoundTripsAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golden.json")

	store, err := golden.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	first, _ := run(t, session.Config{Seed: 42, Games: 40})
	if err := store.Verify(first.Golden()); err != nil {
		t.Fatalf("recording the first run: %v", err)
	}

	// A separate store, reading what the first one persisted.
	reopened, err := golden.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	recorded, ok := reopened.Get(42, 40)
	if !ok {
		t.Fatal("nothing persisted for seed 42")
	}
	if recorded.StateHash != first.StateHash {
		t.Errorf("persisted state root %016x, want %016x", recorded.StateHash, first.StateHash)
	}

	// A fresh run of the same seed validates against it.
	second, _ := run(t, session.Config{Seed: 42, Games: 40})
	if err := reopened.Verify(second.Golden()); err != nil {
		t.Errorf("a clean rerun failed golden verification: %v", err)
	}
}

func TestGoldenRejectsAMismatchedOutcome(t *testing.T) {
	store, err := golden.Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	real, _ := run(t, session.Config{Seed: 42, Games: 40})
	if err := store.Verify(real.Golden()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	tampered := real.Golden()
	tampered.StateHash ^= 1

	err = store.Verify(tampered)
	var mismatch *golden.MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("got %v, want a *golden.MismatchError", err)
	}
	if mismatch.Golden.StateHash != real.StateHash {
		t.Errorf("mismatch reports golden %016x, want %016x", mismatch.Golden.StateHash, real.StateHash)
	}
}

// Different seeds are different records, not a conflict.
func TestGoldenKeysOnSeedAndGameCount(t *testing.T) {
	store, err := golden.Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, cfg := range []session.Config{
		{Seed: 42, Games: 20},
		{Seed: 42, Games: 40},
		{Seed: 43, Games: 20},
	} {
		result, _ := run(t, cfg)
		if err := store.Verify(result.Golden()); err != nil {
			t.Errorf("seed %d over %d games: %v", cfg.Seed, cfg.Games, err)
		}
	}
	if _, ok := store.Get(42, 20); !ok {
		t.Error("seed 42 over 20 games was not recorded")
	}
	if _, ok := store.Get(42, 99); ok {
		t.Error("an unrecorded game count returned a record")
	}
}

func waitForGames(t *testing.T, s *session.Session, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if s.Tracker().Snapshot().Completed >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("tournament did not reach %d games", n)
}

func strategyNames() []string {
	names := make([]string, 0, len(fsm.AllStrategies))
	for _, s := range fsm.AllStrategies {
		names = append(names, string(s))
	}
	return names
}

// The case that broke the claim on the page: bug a replica when there is almost
// nothing logged yet. Replaying the log so far asks it to decide barely anything,
// so it sails through, goes live, wins a race, and puts its wrong answer into the
// log -- at which point the healthy sibling disagrees with the record and
// quarantines itself, and every later restart is refused for disagreeing too.
//
// Rehearsing against the whole canonical tournament closes it: a defect that
// would ever show up has to show up before the candidate is connected to
// anything. Run repeatedly, because the failure it guards against was a race.
func TestBuggedReplicaIsRefusedEvenWithNothingLoggedYet(t *testing.T) {
	ref := reference(t, 42, 40)

	for attempt := 0; attempt < 6; attempt++ {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			s := session.New(session.Config{
				Seed: 42, Games: 40, Replicas: 2, Reference: ref, StartPaused: true,
			})
			s.Start(ctx)

			// Nothing has been admitted at all: the log is empty.
			if err := s.RestartWithBug("flipper", "r1", session.Defect{ImpureClock: true}); err != nil {
				t.Fatalf("attempt %d: %v", attempt, err)
			}
			if got := status(t, s, "flipper", "r1"); !got.Quarantined || got.Running {
				t.Fatalf("attempt %d: bugged replica = %+v, want refused before it ran", attempt, got)
			}
			if got := status(t, s, "flipper", "r0"); got.Quarantined {
				t.Fatalf("attempt %d: the healthy replica was quarantined instead", attempt)
			}

			// The healthy half carries the player, and the outcome is untouched.
			s.Resume()
			result := s.Wait()
			if result.Games != 40 {
				t.Fatalf("attempt %d: completed %d of 40 games", attempt, result.Games)
			}
			if len(result.Quarantined) != 1 || result.Quarantined[0] != "flipper/r1" {
				t.Fatalf("attempt %d: quarantined %v, want exactly [flipper/r1]", attempt, result.Quarantined)
			}
			if got, _ := ref.Root(int64(result.LogLength - 1)); got != result.StateHash {
				t.Fatalf("attempt %d: outcome left the canonical chain", attempt)
			}
		}()
	}
}

// And a clean replica passes rehearsal and rejoins, from the same standing start.
func TestCleanReplicaPassesRehearsalFromAnEmptyLog(t *testing.T) {
	ref := reference(t, 42, 40)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := session.New(session.Config{
		Seed: 42, Games: 40, Replicas: 2, Reference: ref, StartPaused: true,
	})
	s.Start(ctx)

	if err := s.Restart("flipper", "r1"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := status(t, s, "flipper", "r1"); got.Quarantined || !got.Running {
		t.Fatalf("a clean replica was refused: %+v", got)
	}

	s.Resume()
	result := s.Wait()
	if result.Games != 40 {
		t.Errorf("completed %d of 40 games", result.Games)
	}
	if len(result.Quarantined) != 0 {
		t.Errorf("quarantined %v, want none", result.Quarantined)
	}
}

// The defect the UI injects has to be caught every single time. An intermittent
// one is not good enough: rehearsal asks a player for all of its decisions inside
// a loop lasting microseconds, so on a machine whose clock granularity is coarser
// than that loop, a clock-dependent defect reads the same instant every time,
// comes out honest, and is let through -- to diverge later, live, where decisions
// are a second apart. That is a true property of intermittent faults, and a
// terrible thing to hang a demonstration on.
func TestTheInjectedDefectIsRefusedEveryTime(t *testing.T) {
	ref := reference(t, 42, 25)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const tries = 60
	for i := 0; i < tries; i++ {
		s := session.New(session.Config{
			Seed: 42, Games: 25, Replicas: 2, Reference: ref, StartPaused: true,
		})
		s.Start(ctx)

		if err := s.RestartWithBug("flipper", "r1", session.Defect{WrongDecision: true}); err != nil {
			t.Fatalf("try %d: %v", i, err)
		}
		if got := status(t, s, "flipper", "r1"); !got.Quarantined || got.Running {
			t.Fatalf("try %d: bugged replica = %+v, want refused", i, got)
		}
		if got := status(t, s, "flipper", "r0"); got.Quarantined {
			t.Fatalf("try %d: the healthy replica was quarantined instead", i)
		}
		s.Kill("flipper", "r0")
	}
}

// Hammering the button is the reported reproduction: repeated bugged restarts on
// one replica must never let one through, and must never touch its partner.
func TestRepeatedBuggedRestartsNeverLetOneThrough(t *testing.T) {
	ref := reference(t, 42, 25)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := session.New(session.Config{
		Seed: 42, Games: 25, Replicas: 2, Reference: ref, Interval: time.Millisecond,
	})
	s.Start(ctx)

	for i := 0; i < 40; i++ {
		if err := s.RestartWithBug("flipper", "r1", session.Defect{WrongDecision: true}); err != nil {
			break // the tournament finished under us, which is fine
		}
		if got := status(t, s, "flipper", "r1"); !got.Quarantined {
			t.Fatalf("click %d: bugged replica came back running: %+v", i, got)
		}
		if got := status(t, s, "flipper", "r0"); got.Quarantined {
			t.Fatalf("click %d: the healthy partner was quarantined: %+v", i, got)
		}
	}

	// And it is still recoverable afterwards.
	if err := s.Restart("flipper", "r1"); err == nil {
		if got := status(t, s, "flipper", "r1"); got.Quarantined {
			t.Errorf("a clean restart after the hammering was refused: %+v", got)
		}
	}
	s.Wait()
}
