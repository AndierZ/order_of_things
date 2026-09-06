// Command order_of_things runs one deterministic tournament and prints the
// result, verifying it against the golden record for its seed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"text/tabwriter"

	"order_of_things/internal/golden"
	"order_of_things/internal/session"
)

func main() {
	var (
		seed     = flag.Int64("seed", 42, "session seed; the only source of entropy in the system")
		games    = flag.Int("games", 50, "number of games to play")
		replicas = flag.Int("replicas", 2, "instances of every component; 2 is active-active")
		goldPath = flag.String("golden", "testdata/golden.json", "golden outcome store; empty for none")
		bug      = flag.String("bug", "", "inject a determinism bug into one replica, as component/replica (e.g. flipper/r1)")
		quiet    = flag.Bool("quiet", false, "suppress the platform's own logging")
	)
	flag.Parse()

	if *quiet {
		log.SetOutput(io.Discard)
	}

	cfg := session.Config{Seed: *seed, Games: *games, Replicas: *replicas}
	if *bug != "" {
		component, replica, err := parseBug(*bug)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		cfg.Bug = &session.Bug{Component: component, Replica: replica, ImpureClock: true}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	result := session.New(cfg).Run(ctx)
	report(os.Stdout, result)

	if err := verify(*goldPath, result); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n", err)
		os.Exit(1)
	}
}

func parseBug(spec string) (component, replica string, err error) {
	for i := 0; i < len(spec); i++ {
		if spec[i] == '/' {
			return spec[:i], spec[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("bad -bug %q: want component/replica, e.g. flipper/r1", spec)
}

func report(w io.Writer, result session.Result) {
	fmt.Fprintf(w, "seed %d, %d games, %d events\n", result.Seed, result.Games, result.LogLength)
	fmt.Fprintf(w, "state root %016x\n\n", result.StateHash)

	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "STRATEGY\tSCORE")
	for _, entry := range result.Leaderboard {
		fmt.Fprintf(table, "%s\t%d\n", entry.Strategy, entry.Score)
	}
	table.Flush()

	if len(result.Quarantined) > 0 {
		fmt.Fprintf(w, "\nquarantined: %v\n", result.Quarantined)
		fmt.Fprintln(w, "one half of a pair disagreed with the other and refused to keep serving.")
		fmt.Fprintln(w, "this proves they diverged, not which half was right.")
	}
}

func verify(path string, result session.Result) error {
	store, err := golden.Open(path)
	if err != nil {
		return err
	}
	recorded, existed := store.Get(result.Seed, result.Games)

	if err := store.Verify(result.Golden()); err != nil {
		var mismatch *golden.MismatchError
		if errors.As(err, &mismatch) {
			return fmt.Errorf("DIVERGED from the golden outcome:\n  got  %016x\n  want %016x",
				mismatch.Got.StateHash, mismatch.Golden.StateHash)
		}
		return err
	}
	if existed {
		fmt.Printf("\nmatches the golden outcome for seed %d (%016x)\n", result.Seed, recorded.StateHash)
	} else {
		fmt.Printf("\nrecorded as the golden outcome for seed %d\n", result.Seed)
	}
	return nil
}
