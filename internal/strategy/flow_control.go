package strategy

import "order_of_things/internal/fsm"

// ShouldRespond gates which strategy is allowed to emit a decision right now.
//
// Decisions are taken sequentially -- A decides, then B -- rather than
// simultaneously. This is a deliberate departure from a textbook Prisoner's
// Dilemma. With one game in flight and one participant able to speak at a time,
// exactly one component ever has something to send, so admission order is forced
// and the events land in the same order every run.
//
// That is a convenience, not the mechanism, and it is worth not confusing the
// two. Deciding an order globally is far more than any of these strategies
// needs: Flip needs its own games ordered, Retaliate needs a pairing's games
// ordered, and only CopyLeader needs anything global -- and even then only as far
// back as its own game's admission point. Serializing everything is the
// sledgehammer version of that. The correctly-scoped version injects games
// concurrently and has each component keep its own view ordered, buffering what
// arrives early and waiting on a watermark where it genuinely has to; that is
// what v2 is, and it gives up none of the determinism.
//
// It does not make the log identical run to run either, and nothing here does.
// Both replicas of the deciding player still race, and which one wins each
// position varies every time. That race is left alone on purpose: it changes who
// is recorded as having answered, never what the answer was.
//
// None of the strategies read the in-flight game's decisions, so deciding second
// confers no advantage.
func ShouldRespond(currentGame *fsm.Game, s fsm.Strategy) bool {
	return currentGame.NextToMove() == s && s != ""
}
