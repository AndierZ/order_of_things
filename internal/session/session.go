package session

import (
	"context"
	"fmt"
	"sync"

	"order_of_things/internal/fsm"
	"order_of_things/internal/golden"
	"order_of_things/internal/injector"
	"order_of_things/internal/platform"
	"order_of_things/internal/strategy"
	"order_of_things/internal/tracker"
)

// Bug names a determinism defect to inject into a single replica, so the
// divergence and quarantine path can be exercised on demand rather than existing
// only as an untested code path.
type Bug struct {
	// Component and Replica select which instance to corrupt. Only ever one:
	// a pair where both replicas share a bug agrees with itself, which is the
	// limitation of active-active the design names explicitly.
	Component string
	Replica   string
	// ImpureClock makes the replica's decision depend on the wall clock. Diverges
	// immediately, including in v1.
	ImpureClock bool
	// SkipWatermark makes CopyLeader read the newest scores rather than the ones
	// scoped to its own game. Dormant in v1; breaks invariant 1 in v2.
	SkipWatermark bool
}

// Config describes one tournament.
type Config struct {
	// Seed is the single injectable source of entropy for the whole session.
	Seed int64
	// Games is how many games to play before the tournament stops.
	Games int
	// Replicas is how many instances of every component to run. 1 is a
	// single-instance system; 2 is the active-active pair the design calls for.
	Replicas int
	// Bug optionally corrupts one replica.
	Bug *Bug
}

func (c Config) withDefaults() Config {
	if c.Replicas < 1 {
		c.Replicas = 1
	}
	if c.Games < 1 {
		c.Games = 1
	}
	return c
}

// component is what the session supervises. Every component is the same shape:
// an event loop over the sequenced stream, plus a state root it can be checked
// against.
type component interface {
	Run(ctx context.Context) error
	StateHash() uint64
}

// slot is one supervised replica: how to build a fresh instance of it, and the
// handle on the instance currently running.
type slot struct {
	component string
	id        string
	build     func() component
	// The tracker is a single long-lived read model rather than one half of an
	// arbitrated pair, so it is supervised but not fault-injectable.
	restartable bool

	cancel  context.CancelFunc
	done    chan error
	running bool
	// killed and quarantined are the two ways a replica leaves the pair. Killed
	// is a simulated fault and is recoverable; quarantined means the replica
	// disagreed with its sibling and refused to keep serving.
	killed      bool
	quarantined bool
}

func (s *slot) String() string { return s.component + "/" + s.id }

// Session is one fully in-memory instance of the whole system: a sequencer, an
// injector, the four strategies and a tracker. Nothing is shared between
// sessions, so a server can run many at once and each stays reproducible from
// its own seed alone.
//
// The mutex here guards supervision bookkeeping -- which replicas are running --
// and nothing else. No state machine is behind it; those stay owned by their own
// goroutines.
type Session struct {
	cfg       Config
	sequencer *platform.Sequencer
	tracker   *tracker.Tracker

	mu            sync.Mutex
	slots         []*slot
	ctx           context.Context
	cancel        context.CancelFunc
	sequencerDone chan struct{}
	started       bool
}

func New(cfg Config) *Session {
	cfg = cfg.withDefaults()
	sequencer := platform.NewSequencer()
	s := &Session{
		cfg:       cfg,
		sequencer: sequencer,
		tracker:   tracker.New("r0", cfg.Games, sequencer),
	}

	// The tracker never plays, so it is never arbitrated and needs no pair.
	s.slots = append(s.slots, &slot{
		component: tracker.Component,
		id:        "r0",
		build:     func() component { return s.tracker },
	})

	for r := 0; r < cfg.Replicas; r++ {
		id := fmt.Sprintf("r%d", r)
		s.add(injector.Component, id, func() component {
			return injector.NewGameInjector(id, cfg.Seed, cfg.Games, sequencer)
		})
		for _, name := range fsm.AllStrategies {
			playerCfg := strategy.Config{}
			if bug := cfg.Bug; bug != nil && bug.Component == string(name) && bug.Replica == id {
				playerCfg = strategy.Config{
					SkipWatermark: bug.SkipWatermark,
					ImpureClock:   bug.ImpureClock,
				}
			}
			s.add(string(name), id, func() component {
				return strategy.New(name, id, sequencer, playerCfg)
			})
		}
	}
	return s
}

func (s *Session) add(component, id string, build func() component) {
	s.slots = append(s.slots, &slot{
		component: component, id: id, build: build, restartable: true,
	})
}

// Tracker is the session's read model, safe to poll while the session runs.
func (s *Session) Tracker() *tracker.Tracker { return s.tracker }

// Sequencer is the session's admission gate. Its log is only safe to read once
// the session has stopped.
func (s *Session) Sequencer() *platform.Sequencer { return s.sequencer }

// Result is what a finished session produced. It is the unit compared against a
// golden outcome, and between v1 and v2.
type Result struct {
	Seed        int64
	Games       int
	Leaderboard []fsm.LeaderboardEntry
	StateHash   uint64
	LogLength   int
	Quarantined []string
}

// Golden converts the result into the durable record for its seed.
func (r Result) Golden() golden.Outcome {
	return golden.Outcome{
		Seed:        r.Seed,
		Games:       r.Games,
		StateHash:   r.StateHash,
		Leaderboard: r.Leaderboard,
	}
}

// Start launches the sequencer and every replica, and returns immediately.
func (s *Session) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(ctx)

	sequencerDone := make(chan struct{})
	go func() {
		defer close(sequencerDone)
		s.sequencer.Run(s.ctx)
	}()
	s.sequencerDone = sequencerDone

	for _, sl := range s.slots {
		s.startLocked(sl)
	}
}

func (s *Session) startLocked(sl *slot) {
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan error, 1)
	instance := sl.build()

	sl.cancel, sl.done, sl.running, sl.killed = cancel, done, true, false
	go func() { done <- instance.Run(ctx) }()
}

// Wait blocks until the tournament reaches its target or ctx is cancelled, then
// stops everything and reports the outcome.
func (s *Session) Wait() Result {
	if !s.started {
		panic("session: Wait called before Start")
	}
	select {
	case <-s.tracker.Done():
	case <-s.ctx.Done():
	}

	// Stop the components, join every goroutine, then stop the sequencer and read
	// its log. Joining before reading is what makes those reads safe without
	// putting a lock on the state itself.
	s.cancel()

	s.mu.Lock()
	quarantined := make([]string, 0)
	for _, sl := range s.slots {
		if sl.running {
			if err := <-sl.done; err != nil {
				sl.quarantined = true
			}
			sl.running = false
		}
		if sl.quarantined {
			quarantined = append(quarantined, sl.String())
		}
	}
	s.mu.Unlock()
	<-s.sequencerDone

	store := s.tracker.GameStore()
	return Result{
		Seed:        s.cfg.Seed,
		Games:       len(store.CompletedGames()),
		Leaderboard: store.Leaderboard(),
		StateHash:   store.StateHash(),
		LogLength:   len(s.sequencer.EventLog()),
		Quarantined: quarantined,
	}
}

// Run starts the session and waits for it to finish.
func (s *Session) Run(ctx context.Context) Result {
	s.Start(ctx)
	return s.Wait()
}

// Kill stops one replica's event loop, simulating a process death. There is no
// OS process to signal in a single-process design, so "killing" means the replica
// stops responding; its sibling carries the component alone.
func (s *Session) Kill(component, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sl, err := s.findLocked(component, id)
	if err != nil {
		return err
	}
	if !sl.restartable {
		return fmt.Errorf("session: %s is not fault-injectable", sl)
	}
	if !sl.running {
		return fmt.Errorf("session: %s is not running", sl)
	}
	sl.cancel()
	if err := <-sl.done; err != nil {
		sl.quarantined = true
	}
	sl.running, sl.killed = false, true
	return nil
}

// Restart brings a killed replica back as a fresh instance with empty state. It
// subscribes, replays the log from the sequencer, and rejoins its pair -- unless
// its replay disagrees with what the log records, in which case it quarantines
// itself before it can affect anything.
func (s *Session) Restart(component, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sl, err := s.findLocked(component, id)
	if err != nil {
		return err
	}
	if !sl.restartable {
		return fmt.Errorf("session: %s is not fault-injectable", sl)
	}
	if sl.running {
		return fmt.Errorf("session: %s is already running", sl)
	}
	if sl.quarantined {
		return fmt.Errorf("session: %s is quarantined and may not rejoin", sl)
	}
	s.startLocked(sl)
	return nil
}

// ReplicaStatus reports what the supervisor knows about one replica.
type ReplicaStatus struct {
	Component   string
	Replica     string
	Running     bool
	Killed      bool
	Quarantined bool
}

// Status lists every supervised replica. Safe from any goroutine.
func (s *Session) Status() []ReplicaStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	statuses := make([]ReplicaStatus, 0, len(s.slots))
	for _, sl := range s.slots {
		statuses = append(statuses, ReplicaStatus{
			Component:   sl.component,
			Replica:     sl.id,
			Running:     sl.running,
			Killed:      sl.killed,
			Quarantined: sl.quarantined,
		})
	}
	return statuses
}

func (s *Session) findLocked(component, id string) (*slot, error) {
	for _, sl := range s.slots {
		if sl.component == component && sl.id == id {
			return sl, nil
		}
	}
	return nil, fmt.Errorf("session: no replica %s/%s", component, id)
}
