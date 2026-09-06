package strategy

import "order_of_things/internal/fsm"

// ShouldRespond gates which strategy is allowed to emit a decision right now.
//
// Decisions are taken sequentially -- A decides, then B -- rather than
// simultaneously. This is a deliberate departure from a textbook Prisoner's
// Dilemma, and it buys a stronger property than outcome determinism: the event
// log itself is byte-identical run to run. If both participants emitted on the
// same NewGame event, the sequencer would order them by whichever goroutine won a
// real-time race, so the same seed would produce the same final scores but a
// different log. None of the strategies read the in-flight game's decisions, so
// deciding second confers no advantage.
func ShouldRespond(currentGame *fsm.Game, s fsm.Strategy) bool {
	return currentGame.NextToMove() == s && s != ""
}
