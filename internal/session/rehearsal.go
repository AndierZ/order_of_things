package session

import (
	"fmt"

	"order_of_things/internal/golden"
	"order_of_things/internal/platform"
)

// RehearsalError reports that a candidate replica failed to reproduce the
// canonical tournament, and where.
type RehearsalError struct {
	Replica string
	Seq     int64
	Reason  string
}

func (e *RehearsalError) Error() string {
	return fmt.Sprintf("session: %s failed rehearsal at seq %d: %s", e.Replica, e.Seq, e.Reason)
}

// rehearse makes a candidate replay the entire canonical tournament in private,
// before it is connected to anything, and reports the first place it disagrees.
//
// Replaying only the log so far is not enough, and the gap is not academic. A
// corrupted *decision* function looks perfect until it is asked to decide, and
// the history a replica happens to rejoin against may never have asked it. A
// replica bugged early in a session has almost nothing to re-derive, sails
// through, and goes live -- where it can win a race and put its wrong answer into
// the log. The healthy sibling then disagrees with what was recorded and
// quarantines *itself*, and from that point the log is off-canonical, so every
// later restart re-derives the correct answer, disagrees with the record, and is
// refused too. One click, both halves dead, unrecoverable.
//
// Rehearsing against the whole canonical tournament closes that: a defect that
// would ever show up has to show up here, where the candidate is talking to
// nobody and can be refused for free.
//
// Worth being honest about what this needs, because it is not a live mechanism.
// A latent decision bug cannot be caught at rejoin time by any means -- it has
// not happened yet -- so the only way to catch it is to ask a question whose
// answer is already known, and production has no canonical future to ask about.
// Where this check belongs in a real system is before deployment: replay a
// recorded trace against the new build and refuse to ship it if it diverges,
// which is what deterministic simulation testing is. Determinism plus a recorded
// trace is what makes having a known answer possible at all.
func rehearse(candidate component, replica string, reference *golden.Validator) error {
	log := reference.Log()
	if len(log) == 0 {
		return nil // nothing canonical to rehearse against
	}

	// Everything this component is supposed to emit, in order.
	expected := make([]any, 0)
	for _, entry := range log {
		if entry.Component == replica {
			expected = append(expected, entry.Payload())
		}
	}

	emitted := 0
	for _, entry := range log {
		event := &platform.Event{
			Header: platform.Header{
				Seq:             entry.Seq,
				SenderComponent: entry.Component,
			},
			Payload: entry.Payload(),
		}

		if payload := candidate.HandleEvent(event); payload != nil {
			if emitted >= len(expected) {
				return &RehearsalError{replica, entry.Seq, fmt.Sprintf(
					"emitted %#v, but the canonical tournament has nothing more from it", payload)}
			}
			if payload != expected[emitted] {
				return &RehearsalError{replica, entry.Seq, fmt.Sprintf(
					"computed %#v, canonical is %#v", payload, expected[emitted])}
			}
			emitted++
		}

		// The decision function may be fine while the transition function is not,
		// so the state root is checked at every step as well.
		if root, ok := reference.Root(entry.Seq); ok && candidate.StateHash() != root {
			return &RehearsalError{replica, entry.Seq, fmt.Sprintf(
				"state root %016x, canonical is %016x", candidate.StateHash(), root)}
		}
	}

	if emitted != len(expected) {
		return &RehearsalError{replica, -1, fmt.Sprintf(
			"produced %d of the %d events the canonical tournament records from it",
			emitted, len(expected))}
	}
	return nil
}
