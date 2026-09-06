package tracker_test

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"order_of_things/internal/fsm"
	"order_of_things/internal/platform"
	"order_of_things/internal/strategy"
	"order_of_things/internal/tracker"
)

// stubPlayer stands in for the strategies that are not implemented yet. Its
// decision is a pure function of its own name, which is enough to exercise the
// platform end to end and to produce a leaderboard with a real ordering.
type stubPlayer struct {
	strategy  fsm.Strategy
	decision  fsm.Decision
	gameStore *fsm.GameStore
	eventloop *platform.Eventloop
}

func newStubPlayer(s fsm.Strategy, d fsm.Decision, replicaId string, seq *platform.Sequencer) *stubPlayer {
	p := &stubPlayer{strategy: s, decision: d, gameStore: fsm.NewGameStore()}
	p.eventloop = platform.NewEventloop(string(s), replicaId, seq, p.handleEvent)
	return p
}

func (p *stubPlayer) handleEvent(e *platform.Event) any {
	if e.IsReplayComplete() {
		return nil
	}
	p.gameStore.ApplyEvent(e.Header.Seq, e.Payload)
	if !strategy.ShouldRespond(p.gameStore.CurrentGame(), p.strategy) {
		return nil
	}
	return fsm.GameDecision{Strategy: p.strategy, Decision: p.decision}
}

// watcher is a passive subscriber. It never emits, so it does not participate in
// the tournament, but it observes the same ordered stream as everyone else and so
// converges on the same leaderboard.
type watcher struct {
	gameStore *fsm.GameStore
	target    int
	done      chan struct{}
	once      sync.Once
	eventloop *platform.Eventloop
}

func newWatcher(target int, seq *platform.Sequencer) *watcher {
	w := &watcher{gameStore: fsm.NewGameStore(), target: target, done: make(chan struct{})}
	w.eventloop = platform.NewEventloop("watcher", "w1", seq, w.handleEvent)
	return w
}

func (w *watcher) handleEvent(e *platform.Event) any {
	if e.IsReplayComplete() {
		return nil
	}
	w.gameStore.ApplyEvent(e.Header.Seq, e.Payload)
	if len(w.gameStore.CompletedGames()) >= w.target {
		w.once.Do(func() { close(w.done) })
	}
	return nil
}

type tournamentResult struct {
	log         []*platform.Event
	leaderboard []fsm.LeaderboardEntry
	completed   int
}

// runTournament wires a whole system and runs it to completion. replicas is the
// number of instances of every component: 1 is a single-instance system, 2 is the
// active-active configuration the design calls for.
func runTournament(t *testing.T, seed int64, games, replicas int) tournamentResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	seq := platform.NewSequencer()
	var wg sync.WaitGroup
	sequencerDone := make(chan struct{})
	go func() {
		defer close(sequencerDone)
		seq.Run(ctx)
	}()

	watch := newWatcher(games, seq)
	run := func(loop func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := loop(ctx); err != nil {
				t.Errorf("component exited with error: %v", err)
			}
		}()
	}
	run(watch.eventloop.Run)

	// Cooperator is the real implementation; the other three are stubs until
	// milestone 2 lands their FSMs.
	decisions := map[fsm.Strategy]fsm.Decision{
		fsm.Flipper:    fsm.Cheat,
		fsm.Retaliator: fsm.Cooperate,
		fsm.CopyLeader: fsm.Cheat,
	}
	for r := 0; r < replicas; r++ {
		replicaId := fmt.Sprintf("r%d", r)
		run(tracker.NewGameTracker(replicaId, seed, games, seq).Run)
		run(strategy.NewCooperator(replicaId, seq).Run)
		for _, s := range fsm.AllStrategies {
			if d, ok := decisions[s]; ok {
				run(newStubPlayer(s, d, replicaId, seq).Run)
			}
		}
	}

	select {
	case <-watch.done:
	case <-ctx.Done():
		t.Fatalf("tournament did not reach %d games before the deadline", games)
	}

	cancel()
	wg.Wait()
	<-sequencerDone

	return tournamentResult{
		log:         seq.EventLog(),
		leaderboard: watch.gameStore.Leaderboard(),
		completed:   len(watch.gameStore.CompletedGames()),
	}
}

func (p *stubPlayer) Run(ctx context.Context) error { return p.eventloop.Run(ctx) }
func (w *watcher) Run(ctx context.Context) error    { return w.eventloop.Run(ctx) }

// logSignature is the part of the log that must be reproducible.
//
// SenderId is deliberately excluded: which replica of a component won the race to
// the sequencer is a real-time outcome and is legitimately nondeterministic. What
// must not vary is the sequence, the emitting component, and the payload.
func logSignature(log []*platform.Event) []string {
	sig := make([]string, 0, len(log))
	for _, e := range log {
		sig = append(sig, fmt.Sprintf("%d|%s|%d|%#v",
			e.Header.Seq, e.Header.SenderComponent, e.Header.SenderSeq, e.Payload))
	}
	return sig
}

func TestTournamentRunsToCompletion(t *testing.T) {
	result := runTournament(t, 42, 25, 1)

	if result.completed != 25 {
		t.Errorf("completed %d games, want 25", result.completed)
	}
	// One NewGame plus two decisions per game.
	if want := 25 * 3; len(result.log) != want {
		t.Errorf("log has %d events, want %d", len(result.log), want)
	}
	for _, e := range result.log {
		if e.Header.SenderComponent == "watcher" {
			t.Error("the passive watcher emitted an event")
		}
	}
}

// Invariants 1 and 2: the same seed must produce the same event history and the
// same outcome, on every run.
func TestSameSeedProducesIdenticalLogAndOutcome(t *testing.T) {
	first := runTournament(t, 42, 25, 1)
	for run := 1; run < 5; run++ {
		next := runTournament(t, 42, 25, 1)
		if !reflect.DeepEqual(logSignature(first.log), logSignature(next.log)) {
			t.Fatalf("run %d produced a different event log", run)
		}
		if !reflect.DeepEqual(first.leaderboard, next.leaderboard) {
			t.Fatalf("run %d leaderboard = %v, want %v", run, next.leaderboard, first.leaderboard)
		}
	}
}

func TestDifferentSeedsProduceDifferentSchedules(t *testing.T) {
	a := runTournament(t, 42, 25, 1)
	b := runTournament(t, 43, 25, 1)
	if reflect.DeepEqual(logSignature(a.log), logSignature(b.log)) {
		t.Error("seeds 42 and 43 produced the same event log; the seed is not reaching the schedule")
	}
}

// Active-active is only safe because the FSMs are deterministic: running two
// replicas of every component must produce exactly the same log as running one.
// The replicas race, so which SenderId appears varies -- nothing else may.
func TestActiveActiveProducesTheSameLogAsSingleInstance(t *testing.T) {
	single := runTournament(t, 42, 25, 1)
	paired := runTournament(t, 42, 25, 2)

	if !reflect.DeepEqual(logSignature(single.log), logSignature(paired.log)) {
		t.Error("the active-active pair produced a different log from the single instance")
	}
	if !reflect.DeepEqual(single.leaderboard, paired.leaderboard) {
		t.Errorf("active-active leaderboard = %v, want %v", paired.leaderboard, single.leaderboard)
	}
}

// Both replicas of a component emit for every position, but the sequencer must
// admit exactly one of them, or the log would contain duplicate decisions.
func TestActiveActiveAdmitsNoDuplicates(t *testing.T) {
	result := runTournament(t, 42, 25, 2)

	if want := 25 * 3; len(result.log) != want {
		t.Fatalf("log has %d events, want %d -- duplicates were admitted", len(result.log), want)
	}
	perComponent := make(map[string]int64)
	for _, e := range result.log {
		want, seen := perComponent[e.Header.SenderComponent]
		if seen && e.Header.SenderSeq != want {
			t.Fatalf("component %q: SenderSeq %d, want %d", e.Header.SenderComponent, e.Header.SenderSeq, want)
		}
		perComponent[e.Header.SenderComponent] = e.Header.SenderSeq + 1
	}
}

// Every component folds the same ordered stream into its own copy of the state
// machine, so they must all agree -- including the watcher, which never played.
func TestAllComponentsConvergeOnTheSameLeaderboard(t *testing.T) {
	result := runTournament(t, 42, 25, 1)

	replay := fsm.NewGameStore()
	for _, e := range result.log {
		replay.ApplyEvent(e.Header.Seq, e.Payload)
	}
	if !reflect.DeepEqual(replay.Leaderboard(), result.leaderboard) {
		t.Errorf("replaying the log gives %v, the live watcher has %v",
			replay.Leaderboard(), result.leaderboard)
	}
}
