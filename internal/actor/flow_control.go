package actor

import "order_of_things/internal/fsm"

// ShouldActorRespond Gate for flow control
func ShouldActorRespond(currentGame *fsm.Game, actor fsm.Actor) bool {
	if currentGame == nil {
		return false
	}
	if currentGame.DecisionA == nil {
		return currentGame.ActorA == actor
	} else if currentGame.DecisionB == nil {
		return currentGame.ActorB == actor
	}

	return false
}
