package fsm

import (
	"reflect"
	"testing"
)

// apply feeds a sequence of payloads through the store, assigning sequence
// numbers the way the sequencer would.
func apply(store *GameStore, payloads ...any) []*Game {
	completed := make([]*Game, 0)
	for i, payload := range payloads {
		if game := store.ApplyEvent(int64(i), payload); game != nil {
			completed = append(completed, game)
		}
	}
	return completed
}

func playGame(store *GameStore, id int64, a, b Strategy, da, db Decision) {
	apply(store,
		NewGame{Id: id, StrategyA: a, StrategyB: b},
		GameDecision{Strategy: a, Decision: da},
		GameDecision{Strategy: b, Decision: db},
	)
}

func TestPayoffMatrix(t *testing.T) {
	tests := []struct {
		name         string
		a, b         Decision
		wantA, wantB int
	}{
		{"both cooperate", Cooperate, Cooperate, 2, 2},
		{"a cooperates, b cheats", Cooperate, Cheat, -1, 3},
		{"a cheats, b cooperates", Cheat, Cooperate, 3, -1},
		{"both cheat", Cheat, Cheat, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewGameStore()
			playGame(store, 0, Cooperator, Flipper, tc.a, tc.b)

			game := store.LastCompletedGame()
			if game == nil {
				t.Fatal("game did not complete")
			}
			if game.PayoffA != tc.wantA || game.PayoffB != tc.wantB {
				t.Errorf("payoffs = (%d, %d), want (%d, %d)",
					game.PayoffA, game.PayoffB, tc.wantA, tc.wantB)
			}
			if got := store.Score(Cooperator); got != tc.wantA {
				t.Errorf("Score(Cooperator) = %d, want %d", got, tc.wantA)
			}
			if got := store.Score(Flipper); got != tc.wantB {
				t.Errorf("Score(Flipper) = %d, want %d", got, tc.wantB)
			}
		})
	}
}

func TestGameCompletesOnlyOnceBothDecisionsLand(t *testing.T) {
	store := NewGameStore()

	apply(store, NewGame{Id: 0, StrategyA: Cooperator, StrategyB: Flipper})
	if store.CurrentGame() == nil {
		t.Fatal("no game in flight after NewGame")
	}

	if got := apply(store, GameDecision{Strategy: Cooperator, Decision: Cooperate}); len(got) != 0 {
		t.Fatal("game completed on a single decision")
	}
	if store.CurrentGame() == nil {
		t.Fatal("game left flight after a single decision")
	}

	got := apply(store, GameDecision{Strategy: Flipper, Decision: Cheat})
	if len(got) != 1 {
		t.Fatalf("completed %d games on the second decision, want 1", len(got))
	}
	if store.CurrentGame() != nil {
		t.Error("completed game is still in flight")
	}
	if store.NextGameId() != 1 {
		t.Errorf("NextGameId = %d, want 1", store.NextGameId())
	}
}

func TestScoresAccumulateAcrossGames(t *testing.T) {
	store := NewGameStore()
	playGame(store, 0, Cooperator, Flipper, Cooperate, Cooperate) // +2 / +2
	playGame(store, 1, Cooperator, Flipper, Cooperate, Cheat)     // -1 / +3

	if got, want := store.Score(Cooperator), 1; got != want {
		t.Errorf("Score(Cooperator) = %d, want %d", got, want)
	}
	if got, want := store.Score(Flipper), 5; got != want {
		t.Errorf("Score(Flipper) = %d, want %d", got, want)
	}
	if got := len(store.CompletedGames()); got != 2 {
		t.Errorf("CompletedGames = %d, want 2", got)
	}
}

func TestDecisionBeforeGamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic on a decision with no game in flight")
		}
	}()
	NewGameStore().ApplyEvent(0, GameDecision{Strategy: Cooperator, Decision: Cooperate})
}

func TestOverlappingGamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic on a second game admitted while one is in flight")
		}
	}()
	store := NewGameStore()
	apply(store,
		NewGame{Id: 0, StrategyA: Cooperator, StrategyB: Flipper},
		NewGame{Id: 1, StrategyA: Retaliator, StrategyB: CopyLeader},
	)
}

// The three history queries are the three coordination tiers from the design
// doc's correspondence table, expressed as scopes over the same event history.
func TestHistoryQueriesScopeToTheRightSlice(t *testing.T) {
	store := NewGameStore()
	playGame(store, 0, Cooperator, Flipper, Cooperate, Cheat)
	playGame(store, 1, Cooperator, Retaliator, Cooperate, Cooperate)
	playGame(store, 2, Flipper, Retaliator, Cheat, Cheat)

	if got := store.LastCompletedGame().Id; got != 2 {
		t.Errorf("LastCompletedGame = %d, want 2", got)
	}
	// Flipper's own history: most recent game it played, whoever against.
	if got := store.LastCompletedGameFor(Flipper).Id; got != 2 {
		t.Errorf("LastCompletedGameFor(Flipper) = %d, want 2", got)
	}
	// Retaliator's pairwise shard: most recent game against this one opponent.
	if got := store.LastCompletedGameBetween(Retaliator, Cooperator).Id; got != 1 {
		t.Errorf("LastCompletedGameBetween(Retaliator, Cooperator) = %d, want 1", got)
	}
	if got := store.LastCompletedGameBetween(Retaliator, Flipper).Id; got != 2 {
		t.Errorf("LastCompletedGameBetween(Retaliator, Flipper) = %d, want 2", got)
	}
	if got := store.LastCompletedGameBetween(Cooperator, CopyLeader); got != nil {
		t.Errorf("LastCompletedGameBetween with no shared history = %v, want nil", got)
	}
	if got := store.LastCompletedGameFor(CopyLeader); got != nil {
		t.Errorf("LastCompletedGameFor(CopyLeader) = %v, want nil", got)
	}
}

func TestGameAccessors(t *testing.T) {
	store := NewGameStore()
	playGame(store, 0, Cooperator, Flipper, Cooperate, Cheat)
	game := store.LastCompletedGame()

	if got := *game.Decision(Cooperator); got != Cooperate {
		t.Errorf("Decision(Cooperator) = %v, want cooperate", got)
	}
	if got := *game.Decision(Flipper); got != Cheat {
		t.Errorf("Decision(Flipper) = %v, want cheat", got)
	}
	if got := game.Decision(Retaliator); got != nil {
		t.Errorf("Decision(Retaliator) = %v, want nil", got)
	}
	if got := game.Opponent(Cooperator); got != Flipper {
		t.Errorf("Opponent(Cooperator) = %v, want flipper", got)
	}
	if got := game.Opponent(Retaliator); got != "" {
		t.Errorf("Opponent(Retaliator) = %q, want empty", got)
	}
	if game.Involves(Retaliator) {
		t.Error("Involves(Retaliator) = true, want false")
	}
}

func TestNoLeaderBeforeAnyGameCompletes(t *testing.T) {
	store := NewGameStore()
	if got := store.LeadingStrategy(); got != "" {
		t.Errorf("LeadingStrategy = %q, want empty", got)
	}
	apply(store, NewGame{Id: 0, StrategyA: Cooperator, StrategyB: Flipper})
	if got := store.LeadingStrategy(); got != "" {
		t.Errorf("LeadingStrategy mid-game = %q, want empty", got)
	}
}

func TestLeadingStrategyPicksHighestScore(t *testing.T) {
	store := NewGameStore()
	playGame(store, 0, Cooperator, Flipper, Cooperate, Cheat) // -1 / +3
	playGame(store, 1, Retaliator, CopyLeader, Cheat, Cheat)  // 0 / 0

	if got := store.LeadingStrategy(); got != Flipper {
		t.Errorf("LeadingStrategy = %q, want flipper", got)
	}
}

// CopyLeader reads LeadingStrategy, so a nondeterministic answer here breaks
// invariant 1 for the whole tournament. Ranging over the leaderboard map to find
// a maximum would do exactly that: Go randomizes map iteration order, so tied
// strategies would resolve differently run to run and even call to call.
func TestLeadingStrategyIsDeterministicUnderTies(t *testing.T) {
	store := NewGameStore()
	// Every strategy ends on the same score, so only the tie-break decides.
	playGame(store, 0, Cooperator, Flipper, Cooperate, Cooperate)
	playGame(store, 1, Retaliator, CopyLeader, Cooperate, Cooperate)

	for _, s := range AllStrategies {
		if got := store.Score(s); got != 2 {
			t.Fatalf("Score(%s) = %d, want 2 (test setup)", s, got)
		}
	}

	want := store.LeadingStrategy()
	if want != Cooperator {
		t.Errorf("tie broken to %q, want the lowest name %q", want, Cooperator)
	}
	for i := 0; i < 1000; i++ {
		if got := store.LeadingStrategy(); got != want {
			t.Fatalf("LeadingStrategy returned %q on call %d, %q on call 0", got, i, want)
		}
	}
}

func TestLeaderboardIsRankedAndDeterministic(t *testing.T) {
	store := NewGameStore()
	playGame(store, 0, Cooperator, Flipper, Cooperate, Cheat)    // coop -1, flip +3
	playGame(store, 1, Retaliator, CopyLeader, Cheat, Cooperate) // ret +3, copy -1

	want := []LeaderboardEntry{
		{Strategy: Flipper, Score: 3},
		{Strategy: Retaliator, Score: 3},
		{Strategy: Cooperator, Score: -1},
		{Strategy: CopyLeader, Score: -1},
	}
	for i := 0; i < 1000; i++ {
		if got := store.Leaderboard(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Leaderboard on call %d = %v, want %v", i, got, want)
		}
	}
}

func TestAppliedSeqTracksTheWatermark(t *testing.T) {
	store := NewGameStore()
	if got := store.AppliedSeq(); got != -1 {
		t.Errorf("AppliedSeq on an empty store = %d, want -1", got)
	}
	store.ApplyEvent(7, NewGame{Id: 0, StrategyA: Cooperator, StrategyB: Flipper})
	if got := store.AppliedSeq(); got != 7 {
		t.Errorf("AppliedSeq = %d, want 7", got)
	}
	if got := store.CurrentGame().Seq; got != 7 {
		t.Errorf("CurrentGame().Seq = %d, want 7", got)
	}
}

// Replaying the same event history into a fresh store must reproduce the same
// state, byte for byte. This is invariant 1 at the level of a single component.
func TestReplayReproducesIdenticalState(t *testing.T) {
	history := []any{
		NewGame{Id: 0, StrategyA: Cooperator, StrategyB: Flipper},
		GameDecision{Strategy: Cooperator, Decision: Cooperate},
		GameDecision{Strategy: Flipper, Decision: Cheat},
		NewGame{Id: 1, StrategyA: Retaliator, StrategyB: CopyLeader},
		GameDecision{Strategy: Retaliator, Decision: Cheat},
		GameDecision{Strategy: CopyLeader, Decision: Cheat},
		NewGame{Id: 2, StrategyA: Flipper, StrategyB: Retaliator},
		GameDecision{Strategy: Flipper, Decision: Cooperate},
		GameDecision{Strategy: Retaliator, Decision: Cooperate},
	}

	live := NewGameStore()
	apply(live, history...)

	replayed := NewGameStore()
	apply(replayed, history...)

	if !reflect.DeepEqual(live.Leaderboard(), replayed.Leaderboard()) {
		t.Errorf("leaderboards differ: %v vs %v", live.Leaderboard(), replayed.Leaderboard())
	}
	if live.LeadingStrategy() != replayed.LeadingStrategy() {
		t.Errorf("leaders differ: %q vs %q", live.LeadingStrategy(), replayed.LeadingStrategy())
	}
	if !reflect.DeepEqual(live.CompletedGames(), replayed.CompletedGames()) {
		t.Error("completed games differ")
	}
	if live.NextGameId() != replayed.NextGameId() || live.AppliedSeq() != replayed.AppliedSeq() {
		t.Error("store cursors differ")
	}
}
