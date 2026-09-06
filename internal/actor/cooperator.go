package actor

import (
	"context"
	"order_of_things/internal/fsm"

	"golang.design/x/chann"

	"order_of_things/internal/platform"
)

type Cooperator struct {
	actor     fsm.Actor
	eventloop *platform.Eventloop
	gameStore *fsm.GameStore
}

func NewCooperator(
	componentId string,
	ingressCh *chann.Chann[*platform.Event],
) *Cooperator {
	c := &Cooperator{
		actor:     fsm.Cooperator,
		gameStore: fsm.NewGameStore(),
	}
	eventloop := platform.NewEventloop(
		string(c.actor),
		componentId,
		ingressCh,
		c.HandleEvent,
	)
	c.eventloop = eventloop
	return c
}

func (r *Cooperator) Run(ctx context.Context) {
	r.eventloop.Run(ctx)
}

func (r *Cooperator) HandleEvent(e *platform.Event) any {
	r.gameStore.ApplyEvent(e.Payload)
	if ShouldActorRespond(r.gameStore.GetCurrentGame(), r.actor) {
		return fsm.GameDecision{
			Actor:    r.actor,
			Decision: fsm.Cooperate,
		}
	}
	return nil
}
