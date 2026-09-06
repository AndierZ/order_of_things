package strategy

import (
	"testing"

	"order_of_things/internal/fsm"
	"order_of_things/internal/platform"
)

func sequenced(seq int64, payload any) *platform.Event {
	return &platform.Event{Header: platform.Header{Seq: seq}, Payload: payload}
}

func decision(d fsm.Decision) *fsm.Decision { return &d }

func TestShouldRespondSerializesTheTwoParticipants(t *testing.T) {
	game := func(a, b *fsm.Decision) *fsm.Game {
		return &fsm.Game{
			Id: 0, StrategyA: fsm.Cooperator, DecisionA: a,
			StrategyB: fsm.Flipper, DecisionB: b,
		}
	}
	cooperated := decision(fsm.Cooperate)

	tests := []struct {
		name string
		game *fsm.Game
		who  fsm.Strategy
		want bool
	}{
		{"no game in flight", nil, fsm.Cooperator, false},
		{"a's turn", game(nil, nil), fsm.Cooperator, true},
		{"b must wait for a", game(nil, nil), fsm.Flipper, false},
		{"b's turn once a has decided", game(cooperated, nil), fsm.Flipper, true},
		{"a does not decide twice", game(cooperated, nil), fsm.Cooperator, false},
		{"nobody acts on a resolved game", game(cooperated, cooperated), fsm.Flipper, false},
		{"bystanders never act", game(nil, nil), fsm.Retaliator, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRespond(tc.game, tc.who); got != tc.want {
				t.Errorf("ShouldRespond = %v, want %v", got, tc.want)
			}
		})
	}
}

// Cooperator is tier 0: its decision reads none of the shared state, so the same
// event always produces the same answer regardless of history.
func TestCooperatorAlwaysCooperates(t *testing.T) {
	c := &Cooperator{strategy: fsm.Cooperator, gameStore: fsm.NewGameStore()}

	emitted := 0
	for seq, payload := range []any{
		fsm.NewGame{Id: 0, StrategyA: fsm.Cooperator, StrategyB: fsm.Flipper},
		fsm.GameDecision{Strategy: fsm.Cooperator, Decision: fsm.Cooperate},
		fsm.GameDecision{Strategy: fsm.Flipper, Decision: fsm.Cheat},
		fsm.NewGame{Id: 1, StrategyA: fsm.Flipper, StrategyB: fsm.Retaliator},
		fsm.GameDecision{Strategy: fsm.Flipper, Decision: fsm.Cheat},
		fsm.GameDecision{Strategy: fsm.Retaliator, Decision: fsm.Cheat},
		fsm.NewGame{Id: 2, StrategyA: fsm.Retaliator, StrategyB: fsm.Cooperator},
		fsm.GameDecision{Strategy: fsm.Retaliator, Decision: fsm.Cheat},
	} {
		out := c.HandleEvent(sequenced(int64(seq), payload))
		if out == nil {
			continue
		}
		emitted++
		got, ok := out.(fsm.GameDecision)
		if !ok {
			t.Fatalf("emitted %T, want fsm.GameDecision", out)
		}
		if got.Strategy != fsm.Cooperator || got.Decision != fsm.Cooperate {
			t.Errorf("emitted %+v, want cooperator/cooperate", got)
		}
	}
	// Game 0 as participant A, game 2 as participant B; never for game 1, where it
	// is not a participant, and never twice for the same game.
	if emitted != 2 {
		t.Errorf("emitted %d decisions, want 2", emitted)
	}
}
