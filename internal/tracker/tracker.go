package tracker

import (
	"context"
	"sync/atomic"

	"order_of_things/internal/fsm"
	"order_of_things/internal/platform"
)

// Component is the sequencer-level component name for the tracker.
const Component = "game-tracker"

// Snapshot is an immutable view of the tournament, published for readers outside
// the event loop.
type Snapshot struct {
	Seq         int64
	Completed   int
	Leaderboard []fsm.LeaderboardEntry
	Recent      []*fsm.Game
	StateHash   uint64
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

	target int
	done   chan struct{}
	closed bool
}

// New builds a tracker. target is the number of completed games after which Done
// fires; 0 means never.
func New(replicaId string, target int, sequencer *platform.Sequencer) *Tracker {
	t := &Tracker{
		gameStore: fsm.NewGameStore(),
		target:    target,
		done:      make(chan struct{}),
	}
	t.snapshot.Store(&Snapshot{Seq: -1, Leaderboard: []fsm.LeaderboardEntry{}})
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

const recentGames = 12

func (t *Tracker) HandleEvent(e *platform.Event) any {
	if e.IsReplayComplete() {
		return nil
	}
	t.gameStore.ApplyEvent(e.Header.Seq, e.Payload)

	completed := t.gameStore.CompletedGames()
	recent := completed
	if len(recent) > recentGames {
		recent = recent[len(recent)-recentGames:]
	}
	t.snapshot.Store(&Snapshot{
		Seq:         t.gameStore.AppliedSeq(),
		Completed:   len(completed),
		Leaderboard: t.gameStore.Leaderboard(),
		Recent:      recent,
		StateHash:   t.gameStore.StateHash(),
	})

	if t.target > 0 && len(completed) >= t.target && !t.closed {
		t.closed = true
		close(t.done)
	}
	return nil
}
