package actor

import (
	"context"
	"log"
	"math/rand"
	"order_of_things/internal/fsm"

	"golang.design/x/chann"

	"order_of_things/internal/platform"
)

const defaultSeed = 42

type GameTracker struct {
	nextGameId int64
	randGen    *rand.Rand
	eventloop  *platform.Eventloop
	gameStore  *fsm.GameStore
}

func NewGameTracker(
	component string,
	componentId string,
	ingressCh *chann.Chann[*platform.Event],
) *GameTracker {
	c := &GameTracker{
		nextGameId: 0,
		randGen:    rand.New(rand.NewSource(defaultSeed)),
		gameStore:  fsm.NewGameStore(),
	}
	eventloop := platform.NewEventloop(
		component,
		componentId,
		ingressCh,
		c.HandleEvent,
	)
	c.eventloop = eventloop
	return c
}

func (r *GameTracker) Run(ctx context.Context) {
	// Send the genesis event
	r.eventloop.SendExternalPayload(r.newGame())
	r.eventloop.Run(ctx)
}

func (r *GameTracker) HandleEvent(e *platform.Event) any {
	completedGame := r.gameStore.ApplyEvent(e.Payload)

	if completedGame != nil {
		// TODO: Report latest scores for everyone
		log.Println("Game completed for ", r.nextGameId)
		// Create a new game
		return r.newGame()
	}

	return nil
}

func (r *GameTracker) newGame() fsm.NewGame {
	game := fsm.NewGame{
		Id:     r.nextGameId,
		ActorA: r.pickRandomActor(),
		ActorB: r.pickRandomActor(),
	}
	r.nextGameId = r.nextGameId + 1
	return game
}

func (r *GameTracker) pickRandomActor() fsm.Actor {
	randPick := r.randGen.Float64()
	if randPick < 0.25 {
		return fsm.Cooperator
	} else if randPick < 0.5 {
		return fsm.Flipper
	} else if randPick < 0.75 {
		return fsm.Retaliator
	} else {
		return fsm.CopyLeader
	}
}
