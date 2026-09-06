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
		defect   = flag.String("defect", "clock", "which bug to inject: clock (wrong decisions) or payoff (wrong state)")
		interval = flag.Duration("interval", 0, "pace admission, one event per interval; 0 runs flat out")
		quiet    = flag.Bool("quiet", false, "suppress the platform's own logging")
	)
	flag.Parse()

	if *quiet {
		log.SetOutput(io.Discard)
	}

	cfg := session.Config{
		Seed: *seed, Games: *games, Replicas: *replicas, Interval: *interval,
	}
	if *bug != "" {
		component, replica, err := parseBug(*bug)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		injected, err := parseDefect(*defect)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		cfg.Bug = &session.Bug{Component: component, Replica: replica, Defect: injected}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// A defective replica is only caught by the state-root chain if there is one
	// to compare against, so establish the reference before running.
	store, err := golden.Open(*goldPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if cfg.Bug != nil && cfg.Bug.CorruptPayoff {
		if cfg.Reference, err = reference(ctx, store, cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	result := session.New(cfg).Run(ctx)
	report(os.Stdout, result)

	if err := verify(store, result); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n", err)
		os.Exit(1)
	}
}

// reference establishes the canonical chain for a seed by running the tournament
// clean, so that a defective replica has something independent to be checked
// against. The pair alone cannot do this: two replicas sharing a defect agree
// with each other while both being wrong.
func reference(ctx context.Context, store *golden.Store, cfg session.Config) (*golden.Validator, error) {
	if validator := store.Validator(cfg.Seed, cfg.Games); validator != nil {
		return validator, nil
	}
	clean := session.New(session.Config{Seed: cfg.Seed, Games: cfg.Games, Replicas: 1}).Run(ctx)
	if clean.Games != cfg.Games {
		return nil, fmt.Errorf("reference run reached only %d of %d games", clean.Games, cfg.Games)
	}
	if err := store.Record(clean.Golden()); err != nil {
		return nil, err
	}
	return store.Validator(cfg.Seed, cfg.Games), nil
}

func parseDefect(name string) (session.Defect, error) {
	switch name {
	case "clock":
		// Wrong decisions, right state. Caught by comparing this replica's
		// emissions against what the log recorded.
		return session.Defect{ImpureClock: true}, nil
	case "payoff":
		// Right decisions, wrong state. Invisible to the pair; caught only by the
		// canonical chain.
		return session.Defect{CorruptPayoff: true}, nil
	default:
		return session.Defect{}, fmt.Errorf("bad -defect %q: want clock or payoff", name)
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
		fmt.Fprintln(w, "a replica refused to keep serving after disagreeing with what the log recorded.")
	}
}

func verify(store *golden.Store, result session.Result) error {
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
