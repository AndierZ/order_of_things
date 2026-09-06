package fsm

import "sort"

// Game is the materialized state of a single pairwise game.
type Game struct {
	Id        int64
	Seq       int64
	StrategyA Strategy
	DecisionA *Decision
	PayoffA   int
	StrategyB Strategy
	DecisionB *Decision
	PayoffB   int
}

// Decision returns the decision made by s in this game, or nil if s is not in
// this game or has not decided yet.
func (g *Game) Decision(s Strategy) *Decision {
	switch s {
	case g.StrategyA:
		return g.DecisionA
	case g.StrategyB:
		return g.DecisionB
	}
	return nil
}

// Opponent returns the other participant, or "" if s is not in this game.
func (g *Game) Opponent(s Strategy) Strategy {
	switch s {
	case g.StrategyA:
		return g.StrategyB
	case g.StrategyB:
		return g.StrategyA
	}
	return ""
}

// Involves reports whether s is a participant.
func (g *Game) Involves(s Strategy) bool {
	return g.StrategyA == s || g.StrategyB == s
}

// GameStore is the replicated state machine every component runs. It is fed the
// same globally ordered event stream, so every component -- and every replica of
// every component -- holds an identical copy. All transitions here must be pure.
type GameStore struct {
	currentGame    *Game
	leaderboard    map[Strategy]int
	completedGames []*Game
	nextGameId     int64
	appliedSeq     int64
}

func NewGameStore() *GameStore {
	return &GameStore{
		currentGame:    nil,
		leaderboard:    make(map[Strategy]int),
		completedGames: make([]*Game, 0),
		nextGameId:     0,
		appliedSeq:     -1,
	}
}

// ApplyEvent folds one sequenced event into the store. It returns the game that
// this event completed, or nil if no game completed.
func (g *GameStore) ApplyEvent(seq int64, payload any) *Game {
	g.appliedSeq = seq

	switch v := payload.(type) {
	case NewGame:
		if g.currentGame != nil {
			panic("new game admitted while a game is still in flight")
		}
		g.currentGame = &Game{
			Id:        v.Id,
			Seq:       seq,
			StrategyA: v.StrategyA,
			StrategyB: v.StrategyB,
		}
		g.nextGameId = v.Id + 1

	case GameDecision:
		if g.currentGame == nil {
			panic("game decision received before game is created")
		}
		decision := v.Decision
		switch {
		case v.Strategy == g.currentGame.StrategyA && g.currentGame.DecisionA == nil:
			g.currentGame.DecisionA = &decision
		case v.Strategy == g.currentGame.StrategyB && g.currentGame.DecisionB == nil:
			g.currentGame.DecisionB = &decision
		}
		if g.currentGame.DecisionA != nil && g.currentGame.DecisionB != nil {
			completed := g.currentGame
			g.currentGame = nil
			calculatePayoff(completed)
			g.leaderboard[completed.StrategyA] += completed.PayoffA
			g.leaderboard[completed.StrategyB] += completed.PayoffB
			g.completedGames = append(g.completedGames, completed)
			return completed
		}
	}
	return nil
}

func calculatePayoff(game *Game) {
	switch {
	case *game.DecisionA == Cooperate && *game.DecisionB == Cooperate:
		game.PayoffA, game.PayoffB = 2, 2
	case *game.DecisionA == Cooperate && *game.DecisionB == Cheat:
		game.PayoffA, game.PayoffB = -1, 3
	case *game.DecisionA == Cheat && *game.DecisionB == Cooperate:
		game.PayoffA, game.PayoffB = 3, -1
	default: // both cheat: nobody scores
		game.PayoffA, game.PayoffB = 0, 0
	}
}

// CurrentGame returns the game in flight, or nil.
func (g *GameStore) CurrentGame() *Game {
	return g.currentGame
}

// NextGameId is the id the next admitted game should carry. Derived from applied
// state rather than from emission count, so a replica rebuilding from the log
// lands on the same value as the live replica.
func (g *GameStore) NextGameId() int64 {
	return g.nextGameId
}

// AppliedSeq is the sequence number of the last applied event, or -1 if none.
// This is the watermark: state read from this store is state as of AppliedSeq.
func (g *GameStore) AppliedSeq() int64 {
	return g.appliedSeq
}

// CompletedGames returns the completed games in sequence order.
func (g *GameStore) CompletedGames() []*Game {
	return g.completedGames
}

// LastCompletedGame returns the most recently completed game, or nil.
func (g *GameStore) LastCompletedGame() *Game {
	if len(g.completedGames) == 0 {
		return nil
	}
	return g.completedGames[len(g.completedGames)-1]
}

// LastCompletedGameFor returns the most recent completed game involving s, or
// nil. This is the whole state Flipper needs: its own history, nobody else's.
func (g *GameStore) LastCompletedGameFor(s Strategy) *Game {
	for i := len(g.completedGames) - 1; i >= 0; i-- {
		if g.completedGames[i].Involves(s) {
			return g.completedGames[i]
		}
	}
	return nil
}

// LastCompletedGameBetween returns the most recent completed game between s and
// opponent, or nil. This is the whole state Retaliator needs: the (s, opponent)
// pair's history, which is a natural two-party shard.
func (g *GameStore) LastCompletedGameBetween(s, opponent Strategy) *Game {
	for i := len(g.completedGames) - 1; i >= 0; i-- {
		game := g.completedGames[i]
		if game.Involves(s) && game.Involves(opponent) {
			return game
		}
	}
	return nil
}

// Score returns s's total score across all completed games.
func (g *GameStore) Score(s Strategy) int {
	return g.leaderboard[s]
}

// LeadingStrategy returns the highest-scoring strategy as of AppliedSeq, with
// ties broken by name. It returns "" before any game has completed.
//
// The tie-break is not cosmetic. Ranging over the leaderboard map to find a
// maximum would make the answer depend on Go's randomized map iteration order,
// which breaks invariant 1 the same way a time.Now call would -- and CopyLeader
// reads exactly this value, so it would break the whole tournament.
func (g *GameStore) LeadingStrategy() Strategy {
	if len(g.completedGames) == 0 {
		return ""
	}
	best := Strategy("")
	bestScore := 0
	for _, entry := range g.Leaderboard() {
		if best == "" || entry.Score > bestScore {
			best, bestScore = entry.Strategy, entry.Score
		}
	}
	return best
}

// LeaderboardEntry is one row of the ranked leaderboard.
type LeaderboardEntry struct {
	Strategy Strategy
	Score    int
}

// Leaderboard returns every strategy that has played, ranked by score descending
// and then by name ascending. The ordering is total and deterministic.
func (g *GameStore) Leaderboard() []LeaderboardEntry {
	entries := make([]LeaderboardEntry, 0, len(g.leaderboard))
	for strategy, score := range g.leaderboard {
		entries = append(entries, LeaderboardEntry{Strategy: strategy, Score: score})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].Strategy < entries[j].Strategy
	})
	return entries
}
