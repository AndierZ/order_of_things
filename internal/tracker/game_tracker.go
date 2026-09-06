package tracker

import (
	"context"

	"order_of_things/internal/fsm"
	"order_of_things/internal/platform"
)

// Component is the sequencer-level component name for the tracker. Both replicas
// of the tracker share it; that is the key the sequencer deduplicates on.
const Component = "game-tracker"

// GameTracker owns two jobs that the design doc eventually separates: the
// admission policy (when may the next game be injected) and the scores state
// machine. In v1 the admission policy is trivially "when the previous game has
// fully resolved", so there is never more than one game in flight and global
// total ordering holds by construction.
type GameTracker struct {
	seed      int64
	maxGames  int
	eventloop *platform.Eventloop
	gameStore *fsm.GameStore
}

// NewGameTracker builds a tracker replica. seed makes the pairing schedule
// reproducible: it is the single injectable source of entropy for the whole
// system. maxGames bounds the tournament; 0 runs forever.
func NewGameTracker(
	replicaId string,
	seed int64,
	maxGames int,
	sequencer *platform.Sequencer,
) *GameTracker {
	t := &GameTracker{
		seed:      seed,
		maxGames:  maxGames,
		gameStore: fsm.NewGameStore(),
	}
	t.eventloop = platform.NewEventloop(
		Component,
		replicaId,
		sequencer,
		t.HandleEvent,
	)
	return t
}

func (t *GameTracker) Run(ctx context.Context) error {
	return t.eventloop.Run(ctx)
}

// GameStore exposes the tracker's materialized state. Only safe to read once the
// tracker's event loop has stopped.
func (t *GameTracker) GameStore() *fsm.GameStore {
	return t.gameStore
}

func (t *GameTracker) HandleEvent(e *platform.Event) any {
	if e.IsReplayComplete() {
		// Bootstrap the stream, but only if it is genuinely empty. A replica that
		// joined a tournament already in progress has nothing to bootstrap.
		if t.gameStore.AppliedSeq() < 0 {
			return t.newGame()
		}
		return nil
	}

	if completed := t.gameStore.ApplyEvent(e.Header.Seq, e.Payload); completed != nil {
		if t.maxGames > 0 && len(t.gameStore.CompletedGames()) >= t.maxGames {
			return nil
		}
		return t.newGame()
	}
	return nil
}

// newGame draws the next pairing.
func (t *GameTracker) newGame() fsm.NewGame {
	id := t.gameStore.NextGameId()
	a, b := t.pairing(id)
	return fsm.NewGame{Id: id, StrategyA: a, StrategyB: b}
}

// pairing is a pure function of the session seed and the game's position in the
// stream. There is deliberately no PRNG object anywhere in the tracker.
//
// A stateful generator would be a second, hidden source of entropy that the seed
// does not actually govern: it is advanced by *emissions*, and replicas do not
// emit uniformly. A replica that joins after the first game is admitted never
// draws for it, so from that point on its generator is one step behind its
// sibling's and the two compute different pairings from the same event history --
// which is invariant 1 breaking, even though every individual draw was "seeded".
// Deriving the pairing from (seed, gameId) instead means any replica can compute
// any game's pairing at any time, having observed nothing but the log.
func (t *GameTracker) pairing(gameId int64) (fsm.Strategy, fsm.Strategy) {
	n := uint64(len(fsm.AllStrategies))
	h := mix(uint64(t.seed) ^ mix(uint64(gameId)))

	a := h % n
	// Draw the opponent from the n-1 strategies that are not A, so a strategy can
	// never be paired against itself. Doing this by construction rather than by
	// rejection sampling keeps the draw a fixed-cost pure function.
	b := (h / n) % (n - 1)
	if b >= a {
		b++
	}
	return fsm.AllStrategies[a], fsm.AllStrategies[b]
}

// mix is splitmix64: a fast integer mixer with good avalanche, used here to turn
// (seed, position) into a well-distributed draw without carrying any state.
func mix(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}
