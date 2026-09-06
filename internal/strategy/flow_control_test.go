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
