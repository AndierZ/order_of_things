// Package golden persists the canonical outcome of a seed.
//
// Everything else about a session is in memory and dies with it: the sequencer,
// the components, the event log. That is fine for the log, which is large and
// rederivable, but it leaves nothing to check a replay against. A restarting
// replica can only be validated against a live sibling that happens to still
// exist in the same process, which is exactly the case that has failed if the
// process died.
//
// So one small thing is durable: for seed X over N games, the canonical final
// state root is H. That is enough for a fresh session to prove it reproduced a
// previous run, and enough to catch a replica whose divergence changed the
// outcome rather than merely being detected in flight.
package golden

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"order_of_things/internal/fsm"
)

// Outcome is the canonical result of a tournament.
type Outcome struct {
	Seed        int64                  `json:"seed"`
	Games       int                    `json:"games"`
	StateHash   uint64                 `json:"stateHash"`
	Leaderboard []fsm.LeaderboardEntry `json:"leaderboard"`
}

func (o Outcome) key() string { return fmt.Sprintf("%d:%d", o.Seed, o.Games) }

// MismatchError reports that a run disagreed with the recorded canonical result
// for its seed. The recorded outcome is authoritative; the run is suspect.
type MismatchError struct {
	Golden Outcome
	Got    Outcome
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf(
		"outcome for seed %d over %d games does not match the golden record: state root %016x, want %016x",
		e.Got.Seed, e.Got.Games, e.Got.StateHash, e.Golden.StateHash,
	)
}

// Store is a small durable map from seed to canonical outcome. It is safe for
// concurrent use: unlike the state machines, it is genuinely shared between the
// sessions a server is running at once.
type Store struct {
	mu       sync.Mutex
	path     string
	outcomes map[string]Outcome
}

// Open loads the store at path, creating it if it does not exist. A path of ""
// gives an in-memory store that persists nothing, which is what tests and
// ephemeral sessions want.
func Open(path string) (*Store, error) {
	s := &Store{path: path, outcomes: make(map[string]Outcome)}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("golden: reading %s: %w", path, err)
	}
	var records []Outcome
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("golden: parsing %s: %w", path, err)
	}
	for _, record := range records {
		s.outcomes[record.key()] = record
	}
	return s, nil
}

// Get returns the canonical outcome for a seed and game count, if one is
// recorded.
func (s *Store) Get(seed int64, games int) (Outcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	outcome, ok := s.outcomes[Outcome{Seed: seed, Games: games}.key()]
	return outcome, ok
}

// Verify checks a run against the canonical record, recording it as canonical if
// this is the first run for that seed. First run wins: there is nothing to
// arbitrate against, so the first observation defines the reference and every
// later run is checked against it.
func (s *Store) Verify(got Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.outcomes[got.key()]
	if !ok {
		s.outcomes[got.key()] = got
		// Persist under the same lock that guards the map, so concurrent writers
		// serialize: the file always reflects a consistent snapshot, and two
		// writers can never race over the same temporary file.
		return s.flushLocked()
	}
	if existing.StateHash != got.StateHash {
		return &MismatchError{Golden: existing, Got: got}
	}
	return nil
}

// flushLocked writes the whole store to disk. The caller must hold s.mu.
func (s *Store) flushLocked() error {
	if s.path == "" {
		return nil
	}
	records := make([]Outcome, 0, len(s.outcomes))
	for _, outcome := range s.outcomes {
		records = append(records, outcome)
	}

	// Sorted so the file is stable across writes and readable in a diff.
	sort.Slice(records, func(i, j int) bool {
		if records[i].Seed != records[j].Seed {
			return records[i].Seed < records[j].Seed
		}
		return records[i].Games < records[j].Games
	})
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("golden: encoding: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("golden: creating %s: %w", dir, err)
	}

	// Write to a uniquely named temporary in the same directory and rename over
	// the target, so a crash mid-write cannot leave a truncated reference and a
	// stale temporary from a previous crash cannot be mistaken for this one.
	tmp, err := os.CreateTemp(dir, ".golden-*.json")
	if err != nil {
		return fmt.Errorf("golden: creating a temporary file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("golden: writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("golden: closing %s: %w", tmp.Name(), err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("golden: setting permissions on %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("golden: renaming into %s: %w", s.path, err)
	}
	return nil
}
