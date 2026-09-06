package tracker

import (
	"testing"

	"order_of_things/internal/fsm"
	"order_of_things/internal/platform"
)

func sequenced(seq int64, payload any) *platform.Event {
	return &platform.Event{
		Header:  platform.Header{Seq: seq, SenderComponent: "test", SenderId: "r0"},
		Payload: payload,
	}
}

func newTracker(target int) *Tracker {
	t := &Tracker{
		gameStore: fsm.NewGameStore(),
		events:    make([]LoggedEvent, 0, eventFeed),
		wins:      make(map[string]int),
		target:    target,
		done:      make(chan struct{}),
	}
	t.snapshot.Store(&Snapshot{
		Seq:         -1,
		Leaderboard: []fsm.LeaderboardEntry{},
		Events:      []LoggedEvent{},
		Wins:        map[string]int{},
	})
	return t
}

func playGame(tr *Tracker, seq int64, a, b fsm.Strategy, da, db fsm.Decision) int64 {
	tr.HandleEvent(sequenced(seq, fsm.NewGame{Id: seq, StrategyA: a, StrategyB: b}))
	tr.HandleEvent(sequenced(seq+1, fsm.GameDecision{Strategy: a, Decision: da}))
	tr.HandleEvent(sequenced(seq+2, fsm.GameDecision{Strategy: b, Decision: db}))
	return seq + 3
}

// The tracker is a read model: it folds the stream like everyone else but must
// never emit, so it is never arbitrated and never needs a replica pair.
func TestTrackerNeverEmits(t *testing.T) {
	tr := newTracker(0)
	events := []*platform.Event{
		{Payload: platform.ReplayComplete{}},
		sequenced(0, fsm.NewGame{Id: 0, StrategyA: fsm.Flipper, StrategyB: fsm.Cooperator}),
		sequenced(1, fsm.GameDecision{Strategy: fsm.Flipper, Decision: fsm.Cheat}),
		sequenced(2, fsm.GameDecision{Strategy: fsm.Cooperator, Decision: fsm.Cooperate}),
	}
	for i, e := range events {
		if out := tr.HandleEvent(e); out != nil {
			t.Errorf("event %d: tracker emitted %#v", i, out)
		}
	}
}

func TestSnapshotTracksTheStream(t *testing.T) {
	tr := newTracker(0)
	if got := tr.Snapshot(); got.Seq != -1 || got.Completed != 0 {
		t.Errorf("initial snapshot = %+v, want seq -1 and no games", got)
	}

	seq := playGame(tr, 0, fsm.Flipper, fsm.Cooperator, fsm.Cheat, fsm.Cooperate)
	snapshot := tr.Snapshot()
	if snapshot.Completed != 1 {
		t.Errorf("Completed = %d, want 1", snapshot.Completed)
	}
	if snapshot.Seq != seq-1 {
		t.Errorf("Seq = %d, want %d", snapshot.Seq, seq-1)
	}
	if snapshot.StateHash != tr.gameStore.StateHash() {
		t.Error("snapshot hash does not match the store")
	}
	if len(snapshot.Leaderboard) != 2 || snapshot.Leaderboard[0].Strategy != fsm.Flipper {
		t.Errorf("leaderboard = %v, want flipper leading", snapshot.Leaderboard)
	}
}

// Snapshots are published values, not views onto live state: one taken earlier
// must not change when the store moves on.
func TestSnapshotsAreImmutable(t *testing.T) {
	tr := newTracker(0)
	seq := playGame(tr, 0, fsm.Flipper, fsm.Cooperator, fsm.Cheat, fsm.Cooperate)
	early := tr.Snapshot()

	for i := 0; i < 5; i++ {
		seq = playGame(tr, seq, fsm.Retaliator, fsm.CopyLeader, fsm.Cooperate, fsm.Cooperate)
	}

	if early.Completed != 1 {
		t.Errorf("an earlier snapshot now reports %d games, want 1", early.Completed)
	}
	if tr.Snapshot().Completed != 6 {
		t.Errorf("current snapshot = %d games, want 6", tr.Snapshot().Completed)
	}
}

func TestRecentGamesAreBounded(t *testing.T) {
	tr := newTracker(0)
	seq := int64(0)
	for i := 0; i < recentGames+10; i++ {
		seq = playGame(tr, seq, fsm.Flipper, fsm.Cooperator, fsm.Cheat, fsm.Cooperate)
	}
	snapshot := tr.Snapshot()
	if len(snapshot.Recent) != recentGames {
		t.Errorf("Recent has %d games, want %d", len(snapshot.Recent), recentGames)
	}
	if last := snapshot.Recent[len(snapshot.Recent)-1]; last.Id != seq-3 {
		t.Errorf("last recent game id = %d, want %d", last.Id, seq-3)
	}
}

func TestDoneFiresOnceAtTheTarget(t *testing.T) {
	tr := newTracker(2)
	seq := playGame(tr, 0, fsm.Flipper, fsm.Cooperator, fsm.Cheat, fsm.Cooperate)
	select {
	case <-tr.Done():
		t.Fatal("Done fired after 1 of 2 games")
	default:
	}

	seq = playGame(tr, seq, fsm.Retaliator, fsm.CopyLeader, fsm.Cooperate, fsm.Cooperate)
	select {
	case <-tr.Done():
	default:
		t.Fatal("Done did not fire at the target")
	}

	// Games beyond the target must not close it a second time.
	playGame(tr, seq, fsm.Flipper, fsm.Retaliator, fsm.Cheat, fsm.Cheat)
}

// The event feed is what the UI draws the machine from, and Replica is the only
// field in a snapshot that is not a logical fact -- it is which half of the pair
// won the race, and the only place active-active is visible.
func TestEventFeedRecordsWhoWonEachPosition(t *testing.T) {
	tr := newTracker(0)
	tr.HandleEvent(&platform.Event{
		Header:  platform.Header{Seq: 0, SenderComponent: "game-injector", SenderId: "r1"},
		Payload: fsm.NewGame{Id: 0, StrategyA: fsm.Flipper, StrategyB: fsm.Cooperator},
	})
	tr.HandleEvent(&platform.Event{
		Header:  platform.Header{Seq: 1, SenderComponent: "flipper", SenderId: "r0"},
		Payload: fsm.GameDecision{Strategy: fsm.Flipper, Decision: fsm.Cheat},
	})

	events := tr.Snapshot().Events
	if len(events) != 2 {
		t.Fatalf("feed has %d events, want 2", len(events))
	}
	if events[0].Component != "game-injector" || events[0].Replica != "r1" {
		t.Errorf("event 0 = %+v, want game-injector/r1", events[0])
	}
	if events[0].Kind != "new-game" || events[0].Detail != "#0 flipper vs cooperator" {
		t.Errorf("event 0 description = %q / %q", events[0].Kind, events[0].Detail)
	}
	if events[1].Replica != "r0" || events[1].Kind != "decision" {
		t.Errorf("event 1 = %+v, want a decision from r0", events[1])
	}
	if events[1].Detail != "flipper cheats" {
		t.Errorf("event 1 detail = %q, want %q", events[1].Detail, "flipper cheats")
	}

	wins := tr.Snapshot().Wins
	if wins["game-injector/r1"] != 1 || wins["flipper/r0"] != 1 {
		t.Errorf("wins = %v", wins)
	}
}

func TestEventFeedIsBounded(t *testing.T) {
	tr := newTracker(0)
	seq := int64(0)
	for i := 0; i < eventFeed+20; i++ {
		seq = playGame(tr, seq, fsm.Flipper, fsm.Cooperator, fsm.Cheat, fsm.Cooperate)
	}
	events := tr.Snapshot().Events
	if len(events) != eventFeed {
		t.Fatalf("feed has %d events, want %d", len(events), eventFeed)
	}
	if last := events[len(events)-1].Seq; last != seq-1 {
		t.Errorf("feed ends at seq %d, want %d", last, seq-1)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Fatalf("feed has a gap at %d: %d then %d", i, events[i-1].Seq, events[i].Seq)
		}
	}
}

// Everything published in a snapshot must be a copy, or a reader holding what it
// believes is an immutable view watches it change.
func TestSnapshotCollectionsAreCopies(t *testing.T) {
	tr := newTracker(0)
	seq := playGame(tr, 0, fsm.Flipper, fsm.Cooperator, fsm.Cheat, fsm.Cooperate)
	early := tr.Snapshot()

	if early.CurrentGame != nil {
		t.Fatal("a completed game left something in flight")
	}
	playGame(tr, seq, fsm.Retaliator, fsm.CopyLeader, fsm.Cheat, fsm.Cheat)

	if len(early.Events) != 3 {
		t.Errorf("an earlier snapshot's feed grew to %d events", len(early.Events))
	}
	if early.Wins["test/r0"] != 3 {
		t.Errorf("an earlier snapshot's wins changed to %v", early.Wins)
	}
	if tr.Snapshot().Version <= early.Version {
		t.Error("Version did not advance")
	}
}

func TestSnapshotPublishesTheGameInFlight(t *testing.T) {
	tr := newTracker(0)
	tr.HandleEvent(sequenced(0, fsm.NewGame{Id: 0, StrategyA: fsm.Flipper, StrategyB: fsm.Cooperator}))

	published := tr.Snapshot()
	if published.CurrentGame == nil {
		t.Fatal("no game in flight published")
	}
	if got := published.CurrentGame.NextToMove(); got != fsm.Flipper {
		t.Errorf("NextToMove = %q, want flipper", got)
	}

	tr.HandleEvent(sequenced(1, fsm.GameDecision{Strategy: fsm.Flipper, Decision: fsm.Cheat}))
	if published.CurrentGame.DecisionA != nil {
		t.Error("the published copy changed when the store did")
	}
	if got := tr.Snapshot().CurrentGame.NextToMove(); got != fsm.Cooperator {
		t.Errorf("NextToMove after flipper moved = %q, want cooperator", got)
	}
}
