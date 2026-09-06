package strategy

import (
	"context"

	"order_of_things/internal/fsm"
	"order_of_things/internal/platform"
)

// Cooperator always cooperates.
//
// Tier 0 of the correspondence table: its decision depends on nothing at all --
// not the opponent, not its own history, not the leaderboard. It still runs the
// full replicated state machine, because it needs the shared game state to know
// *when* it is its turn, but its decision function reads none of it.
type Cooperator struct {
	strategy  fsm.Strategy
	eventloop *platform.Eventloop
	gameStore *fsm.GameStore
}

func NewCooperator(replicaId string, sequencer *platform.Sequencer) *Cooperator {
	c := &Cooperator{
		strategy:  fsm.Cooperator,
		gameStore: fsm.NewGameStore(),
	}
	c.eventloop = platform.NewEventloop(
		string(c.strategy),
		replicaId,
		sequencer,
		c.HandleEvent,
	)
	return c
}

func (c *Cooperator) Run(ctx context.Context) error {
	return c.eventloop.Run(ctx)
}

func (c *Cooperator) HandleEvent(e *platform.Event) any {
	if e.IsReplayComplete() {
		return nil
	}
	c.gameStore.ApplyEvent(e.Header.Seq, e.Payload)
	if !ShouldRespond(c.gameStore.CurrentGame(), c.strategy) {
		return nil
	}
	return fsm.GameDecision{
		Strategy: c.strategy,
		Decision: fsm.Cooperate,
	}
}
