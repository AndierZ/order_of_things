package golden

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"order_of_things/internal/fsm"
)

func outcome(seed int64, games int, hash uint64) Outcome {
	return Outcome{
		Seed: seed, Games: games, StateHash: hash,
		Leaderboard: []fsm.LeaderboardEntry{{Strategy: fsm.Flipper, Score: 3}},
	}
}

func TestFirstRunDefinesTheReference(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, ok := store.Get(1, 10); ok {
		t.Error("an empty store returned a record")
	}
	if err := store.Verify(outcome(1, 10, 0xabc)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	got, ok := store.Get(1, 10)
	if !ok || got.StateHash != 0xabc {
		t.Errorf("Get = %+v, %v; want the recorded outcome", got, ok)
	}
	if err := store.Verify(outcome(1, 10, 0xabc)); err != nil {
		t.Errorf("a matching rerun was rejected: %v", err)
	}
}

func TestMismatchIsReported(t *testing.T) {
	store, _ := Open("")
	if err := store.Verify(outcome(1, 10, 0xabc)); err != nil {
		t.Fatalf("first run: %v", err)
	}

	err := store.Verify(outcome(1, 10, 0xdef))
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("got %v, want *MismatchError", err)
	}
	if mismatch.Golden.StateHash != 0xabc || mismatch.Got.StateHash != 0xdef {
		t.Errorf("mismatch = %+v", mismatch)
	}
	// The reference is not overwritten by the run that failed against it.
	if got, _ := store.Get(1, 10); got.StateHash != 0xabc {
		t.Errorf("reference was overwritten: %016x", got.StateHash)
	}
}

func TestSeedAndGameCountAreSeparateRecords(t *testing.T) {
	store, _ := Open("")
	for _, o := range []Outcome{outcome(1, 10, 0x1), outcome(1, 20, 0x2), outcome(2, 10, 0x3)} {
		if err := store.Verify(o); err != nil {
			t.Fatalf("verify %+v: %v", o, err)
		}
	}
	for _, tc := range []struct {
		seed  int64
		games int
		want  uint64
	}{{1, 10, 0x1}, {1, 20, 0x2}, {2, 10, 0x3}} {
		got, ok := store.Get(tc.seed, tc.games)
		if !ok || got.StateHash != tc.want {
			t.Errorf("Get(%d, %d) = %016x, %v; want %016x", tc.seed, tc.games, got.StateHash, ok, tc.want)
		}
	}
}

func TestPersistsAcrossOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "golden.json")

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Verify(outcome(7, 25, 0xfeed)); err != nil {
		t.Fatalf("verify: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get(7, 25)
	if !ok {
		t.Fatal("nothing persisted")
	}
	if got.StateHash != 0xfeed {
		t.Errorf("persisted %016x, want feed", got.StateHash)
	}
	if len(got.Leaderboard) != 1 || got.Leaderboard[0].Strategy != fsm.Flipper {
		t.Errorf("leaderboard did not survive the round trip: %v", got.Leaderboard)
	}

	// A store reopened from disk still rejects a mismatch.
	if err := reopened.Verify(outcome(7, 25, 0xbad)); err == nil {
		t.Error("a mismatched outcome was accepted after reopening")
	}
}

func TestOpenMissingFileIsNotAnError(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, ok := store.Get(1, 1); ok {
		t.Error("a missing file produced records")
	}
}

func TestOpenRejectsACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golden.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("a corrupt reference file was accepted")
	}
}

// The store is the one genuinely shared thing in the system: a server runs many
// sessions at once and they all check themselves against it.
func TestConcurrentVerifyIsSafe(t *testing.T) {
	store, _ := Open(filepath.Join(t.TempDir(), "golden.json"))

	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() { errs <- store.Verify(outcome(int64(i%4), 10, uint64(i%4))) }()
	}
	for i := 0; i < 16; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent verify: %v", err)
		}
	}
	for seed := int64(0); seed < 4; seed++ {
		if got, ok := store.Get(seed, 10); !ok || got.StateHash != uint64(seed) {
			t.Errorf("seed %d: %016x, %v", seed, got.StateHash, ok)
		}
	}
}
