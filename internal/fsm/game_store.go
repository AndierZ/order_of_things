package fsm

// Game Materialized state
type Game struct {
	Id        int64
	ActorA    Actor
	DecisionA *Decision
	PayoffA   int
	ActorB    Actor
	DecisionB *Decision
	PayoffB   int
}

type GameStore struct {
	currentGame    *Game
	leaderboard    map[Actor]int
	completedGames []*Game
}

func NewGameStore() *GameStore {
	return &GameStore{
		currentGame:    nil,
		leaderboard:    make(map[Actor]int),
		completedGames: make([]*Game, 0),
	}
}

func (g *GameStore) ApplyEvent(payload any) *Game {
	switch v := payload.(type) {
	case NewGame:
		g.currentGame = &Game{
			Id:     v.Id,
			ActorA: v.ActorA,
			ActorB: v.ActorB,
		}
	case GameDecision:
		if g.currentGame == nil {
			panic("game decision received before game is created")
		} else if v.Actor == g.currentGame.ActorA {
			g.currentGame.DecisionA = &v.Decision
		} else if v.Actor == g.currentGame.ActorB {
			g.currentGame.DecisionB = &v.Decision
		}
		if g.currentGame.DecisionA != nil && g.currentGame.DecisionB != nil {
			calculatePayoff(g.currentGame)
			completedGame := g.currentGame
			g.currentGame = nil

			g.leaderboard[completedGame.ActorA] += completedGame.PayoffA
			g.leaderboard[completedGame.ActorB] += completedGame.PayoffB
			g.completedGames = append(g.completedGames, completedGame)
			return completedGame
		}
	}
	return nil
}

func calculatePayoff(game *Game) {
	decisionA := *game.DecisionA
	decisionB := *game.DecisionB
	if decisionA == Cooperate && decisionB == Cooperate {
		game.PayoffA += 2
		game.PayoffB += 2
	} else if decisionA == Cooperate && decisionB == Cheat {
		game.PayoffA += -1
		game.PayoffB += 3
	} else if decisionA == Cheat && decisionB == Cooperate {
		game.PayoffA += 3
		game.PayoffB += -1
	} else if decisionA == Cheat && decisionB == Cheat {
		// 0 point for both. NOOP
	}
}

func (g *GameStore) GetCurrentGame() *Game {
	return g.currentGame
}

func (g *GameStore) GetLastCompletedGame() *Game {
	// TODO: Return the last completed game
	return nil
}

func (g *GameStore) GetLastCompletedGameForActor(actor string) *Game {
	// TODO: Return the last completed game that involves the given actor
	return nil
}

func (g *GameStore) GetLastCompletedGameForActorAndOpponent(actor, opponentActor string) *Game {
	// TODO: Return the last completed game that involves the given opponent actor
	return nil
}

func (g *GameStore) GetLeadingActor() string {
	// TODO: return the actor with the highest score based on all completed games
	return ""
}
