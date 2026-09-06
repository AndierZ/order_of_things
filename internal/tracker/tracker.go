package tracker

import (
	"context"
	"fmt"
	"sync/atomic"

	"order_of_things/internal/fsm"
	"order_of_things/internal/platform"
)

// Component is the sequencer-level component name for the tracker.
const Component = "game-tracker"

// LoggedEvent is one admitted event, as the UI renders it.
//
// Replica is the half of the pair that won the race to admit this event, and is
// the only place active-active is visible: the same tournament runs identically
// whichever replica wins, so watching the winner change is watching redundancy
// work. Everything else here is a logical fact; this one field is not.
type LoggedEvent struct {
	Seq       int64  `json:"seq"`
	Component string `json:"component"`
	Replica   string `json:"replica"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
}

// Snapshot is an immutable view of the tournament, published for readers outside
// the event loop.
type Snapshot struct {
	// Version increases on every published snapshot, so a polling client can skip
	// frames it has already drawn.
	Version     uint64
	Seq         int64
	Completed   int
	Leaderboard []fsm.LeaderboardEntry
	// Recent holds completed games, which are never mutated again and so can be
	// shared. CurrentGame is a copy, because the game in flight still is.
	Recent      []*fsm.Game
	CurrentGame *fsm.Game
	Events      []LoggedEvent
	// Wins counts admitted events per "component/replica".
	Wins      map[string]int
	StateHash uint64
}

// Tracker is the scores read model. It subscribes to the stream, folds it into a
// GameStore like every other component, and emits nothing -- it never plays, so
// it never needs to be arbitrated.
//
// It exists to give the world outside the event loop something to read. Rather
// than exposing its store (which is owned by its own goroutine), it publishes an
// immutable Snapshot after every event. That is a different thing from sharing
// mutable state: readers get a value that will never change under them, and the
// store itself is still touched by exactly one goroutine.
type Tracker struct {
	gameStore *fsm.GameStore
	eventloop *platform.Eventloop
	snapshot  atomic.Pointer[Snapshot]

	version uint64
	events  []LoggedEvent
	wins    map[string]int

	target int
	done   chan struct{}
	closed bool
}

// New builds a tracker. target is the number of completed games after which Done
// fires; 0 means never.
func New(replicaId string, target int, sequencer *platform.Sequencer) *Tracker {
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
	t.eventloop = platform.NewEventloop(Component, replicaId, sequencer, t.HandleEvent)
	return t
}

func (t *Tracker) Run(ctx context.Context) error { return t.eventloop.Run(ctx) }

// Snapshot returns the most recently published view. Safe from any goroutine.
func (t *Tracker) Snapshot() Snapshot { return *t.snapshot.Load() }

// Done is closed once the tournament reaches its target number of games.
func (t *Tracker) Done() <-chan struct{} { return t.done }

// GameStore exposes the underlying store. Only safe once Run has returned.
func (t *Tracker) GameStore() *fsm.GameStore { return t.gameStore }

// StateHash is the chained state root as of the last event applied. Prefer
// Snapshot for live reads; this is only safe once Run has returned.
func (t *Tracker) StateHash() uint64 { return t.gameStore.StateHash() }

const (
	recentGames = 12
	// eventFeed is how many admitted events the snapshot carries. A client
	// polling faster than the feed fills cannot miss one; at four events a second
	// this is over ten seconds of slack.
	eventFeed = 50
)

func (t *Tracker) HandleEvent(e *platform.Event) any {
	if e.IsReplayComplete() {
		return nil
	}
	t.gameStore.ApplyEvent(e.Header.Seq, e.Payload)
	t.recordEvent(e)

	completed := t.gameStore.CompletedGames()
	recent := completed
	if len(recent) > recentGames {
		recent = recent[len(recent)-recentGames:]
	}

	t.version++
	t.snapshot.Store(&Snapshot{
		Version:     t.version,
		Seq:         t.gameStore.AppliedSeq(),
		Completed:   len(completed),
		Leaderboard: t.gameStore.Leaderboard(),
		Recent:      recent,
		CurrentGame: t.gameStore.CurrentGame().Clone(),
		Events:      t.eventsSnapshot(),
		Wins:        t.winsSnapshot(),
		StateHash:   t.gameStore.StateHash(),
	})

	if t.target > 0 && len(completed) >= t.target && !t.closed {
		t.closed = true
		close(t.done)
	}
	return nil
}

func (t *Tracker) recordEvent(e *platform.Event) {
	kind, detail := describe(e.Payload)
	t.events = append(t.events, LoggedEvent{
		Seq:       e.Header.Seq,
		Component: e.Header.SenderComponent,
		Replica:   e.Header.SenderId,
		Kind:      kind,
		Detail:    detail,
	})
	if len(t.events) > eventFeed {
		t.events = t.events[len(t.events)-eventFeed:]
	}
	t.wins[e.Header.SenderComponent+"/"+e.Header.SenderId]++
}

func describe(payload any) (kind, detail string) {
	switch v := payload.(type) {
	case fsm.NewGame:
		return "new-game", fmt.Sprintf("#%d %s vs %s", v.Id, v.StrategyA, v.StrategyB)
	case fsm.GameDecision:
		return "decision", fmt.Sprintf("%s %ss", v.Strategy, v.Decision)
	default:
		return "unknown", fmt.Sprintf("%T", payload)
	}
}

// Snapshots are published values, so the slices and maps in them must be copies:
// a reader holding an old snapshot must never see it change underneath.
func (t *Tracker) eventsSnapshot() []LoggedEvent {
	events := make([]LoggedEvent, len(t.events))
	copy(events, t.events)
	return events
}

func (t *Tracker) winsSnapshot() map[string]int {
	wins := make(map[string]int, len(t.wins))
	for replica, n := range t.wins {
		wins[replica] = n
	}
	return wins
}
