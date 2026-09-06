package session_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"order_of_things/internal/golden"
	"order_of_things/internal/session"
)

var update = flag.Bool("update", false, "regenerate the committed canonical tournament")

// goldenPath is the checked-in reference, at the repository root so the CLI and
// the server can point at the same file.
const goldenPath = "../../testdata/golden.json"

// The committed tournament is a regression fixture, not a cache. The runtime
// never reads it -- it regenerates the canonical tournament and validates against
// what it just computed -- so nothing depends on this file being fresh. What it
// is for is making a change to the rules visible: touch a payoff, a strategy, the
// pairing draw or the hashing scheme and this fails, and the diff you commit to
// fix it says exactly what the tournament became.
//
// Regenerate deliberately:
//
//	go test ./internal/session -run Golden -update
//
// and read the diff before committing it. A change here that you did not intend
// is the system telling you it stopped being the same system.
func TestGoldenCanonicalTournament(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := session.New(session.Config{
		Seed:     session.CanonicalSeed,
		Games:    session.DefaultGames,
		Replicas: 1,
	}).Run(ctx)

	if result.Games != session.DefaultGames {
		t.Fatalf("reference run reached %d of %d games", result.Games, session.DefaultGames)
	}
	produced := result.Golden()

	if *update {
		store, err := golden.Open(goldenPath)
		if err != nil {
			t.Fatalf("opening %s: %v", goldenPath, err)
		}
		if err := store.Record(produced); err != nil {
			t.Fatalf("recording: %v", err)
		}
		t.Logf("wrote %s: seed %d, %d games, state checksum %016x",
			goldenPath, produced.Seed, produced.Games, produced.StateHash)
		return
	}

	if _, err := os.Stat(goldenPath); os.IsNotExist(err) {
		t.Fatalf("%s is missing; generate it with `go test ./internal/session -run Golden -update`",
			filepath.Clean(goldenPath))
	}
	store, err := golden.Open(goldenPath)
	if err != nil {
		t.Fatalf("opening %s: %v", goldenPath, err)
	}
	recorded, ok := store.Get(session.CanonicalSeed, session.DefaultGames)
	if !ok {
		t.Fatalf("%s has no record for seed %d over %d games",
			goldenPath, session.CanonicalSeed, session.DefaultGames)
	}

	if recorded.StateHash != produced.StateHash {
		t.Errorf("this build produces a different tournament than the committed one:\n"+
			"  state checksum %016x, committed %016x\n"+
			"  leaderboard %v, committed %v\n"+
			"If that was intended, regenerate with `go test ./internal/session -run Golden -update`.",
			produced.StateHash, recorded.StateHash, produced.Leaderboard, recorded.Leaderboard)
	}
	// The state checksum chains every applied state, so a mismatch above would catch
	// any divergence. These compare the rest of the record, which the root does
	// not cover: a change in the shape of what is stored rather than in what the
	// tournament did.
	if len(recorded.Chain) != len(produced.Chain) {
		t.Errorf("committed chain has %d roots, this build produces %d",
			len(recorded.Chain), len(produced.Chain))
	}
	if len(recorded.Log) != len(produced.Log) {
		t.Errorf("committed log has %d events, this build produces %d",
			len(recorded.Log), len(produced.Log))
	}
	if !reflect.DeepEqual(recorded.Leaderboard, produced.Leaderboard) {
		t.Errorf("committed leaderboard %v, this build produces %v",
			recorded.Leaderboard, produced.Leaderboard)
	}
}
