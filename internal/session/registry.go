package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"order_of_things/internal/golden"
)

// CanonicalSeed fixes which tournament everyone plays.
//
// It is not the point and it is deliberately not a knob. A seeded generator
// producing the same sequence twice is a property of the generator, not of this
// system, and offering it as a control invites the reader to think that is what
// is being demonstrated. What is being demonstrated is that the outcome holds
// while you are actively breaking the thing -- killing replicas mid-game,
// restarting them, letting a corrupted one try to rejoin -- with every component
// on its own goroutine and no coordination beyond the order of the log.
//
// Fixing it also buys something concrete: one canonical state-root chain,
// generated once, that every session can check a recovering replica against.
const CanonicalSeed = 20260906

// DefaultGames is how many games a session plays. Fixed rather than unbounded so
// that a canonical reference for the whole run can be generated up front, which
// is what a restarting replica is validated against. Twenty-five games at one
// event per second is a bit over a minute: long enough to kill something and
// watch it recover, short enough to hold a viewer.
const DefaultGames = 25

// DefaultInterval is the starting pace: one event per second, slow enough to
// follow by eye.
const DefaultInterval = time.Second

// Registry owns the live sessions a server is running. Sessions are fully
// independent -- each has its own sequencer, components and state -- so the only
// thing shared between them is the golden store, which is the one place a
// reference outlives the process that produced it.
type Registry struct {
	golden   *golden.Store
	games    int
	interval time.Duration
	// now is injectable so tests do not depend on the clock.
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]*entry
	nextId   int
}

type entry struct {
	id      string
	session *Session
	created time.Time
	touched time.Time
	cancel  context.CancelFunc
	done    chan struct{}
	result  Result
}

// NewRegistry returns a registry recording references in store, which may be an
// in-memory store.
func NewRegistry(store *golden.Store) *Registry {
	return &Registry{
		golden:   store,
		games:    DefaultGames,
		interval: DefaultInterval,
		now:      time.Now,
		sessions: make(map[string]*entry),
	}
}

// Prepare generates the canonical state-root chain up front, so the first session
// does not pay for it and every session -- including one whose replicas are being
// restarted seconds after it starts -- has something to check a recovering
// replica against from its very first event.
func (r *Registry) Prepare(ctx context.Context) error {
	r.mu.Lock()
	games := r.games
	r.mu.Unlock()
	_, err := r.reference(ctx, CanonicalSeed, games)
	return err
}

// SetGames overrides how many games new sessions play. A real server wants the
// default, so that every session has a reference of the same shape.
func (r *Registry) SetGames(games int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.games = games
}

// SetInterval overrides the starting pace of new sessions. Existing sessions keep
// theirs; the pace is a per-session control once it is running.
func (r *Registry) SetInterval(interval time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interval = interval
}

// Handle is a live session and its identity.
type Handle struct {
	Id      string
	Seed    int64
	Games   int
	Session *Session
}

// Create starts a new session. A seed of 0 plays the canonical tournament, which
// is what a server always wants; passing one explicitly is for tests that need
// two different tournaments.
//
// If the canonical chain has not been generated yet, it is generated here. Call
// Prepare at startup to get that out of the way.
func (r *Registry) Create(ctx context.Context, seed int64) (*Handle, error) {
	if seed == 0 {
		seed = CanonicalSeed
	}
	r.mu.Lock()
	games, interval := r.games, r.interval
	r.mu.Unlock()

	reference, err := r.reference(ctx, seed, games)
	if err != nil {
		return nil, err
	}

	live := New(Config{
		Seed:        seed,
		Games:       games,
		Replicas:    2,
		Interval:    interval,
		Reference:   reference,
		StartPaused: true,
	})

	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	live.Start(sessionCtx)

	r.mu.Lock()
	r.nextId++
	id := fmt.Sprintf("s%d", r.nextId)
	e := &entry{
		id:      id,
		session: live,
		created: r.now(),
		touched: r.now(),
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	r.sessions[id] = e
	r.mu.Unlock()

	go func() {
		defer close(e.done)
		result := live.Wait()
		r.mu.Lock()
		e.result = result
		r.mu.Unlock()
	}()

	return &Handle{Id: id, Seed: seed, Games: games, Session: live}, nil
}

// reference returns the canonical chain for a seed, generating it if this is the
// first time the seed has been seen.
func (r *Registry) reference(ctx context.Context, seed int64, games int) (*golden.Validator, error) {
	if validator := r.golden.Validator(seed, games); validator != nil {
		return validator, nil
	}

	// Unpaced, single instance, no defects, and explicitly no validator -- there
	// is nothing to validate against yet, and this run is what defines it.
	result := New(Config{Seed: seed, Games: games, Replicas: 1}).Run(ctx)
	if result.Games != games {
		return nil, fmt.Errorf("session: reference run for seed %d reached only %d of %d games",
			seed, result.Games, games)
	}
	if err := r.golden.Record(result.Golden()); err != nil {
		return nil, err
	}
	return r.golden.Validator(seed, games), nil
}

// Get returns a live session by id, marking it as recently used.
func (r *Registry) Get(id string) (*Handle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.sessions[id]
	if !ok {
		return nil, false
	}
	e.touched = r.now()
	return &Handle{
		Id:      e.id,
		Seed:    e.session.Seed(),
		Games:   e.session.Games(),
		Session: e.session,
	}, true
}

// Stop ends a session and removes it. Idempotent.
func (r *Registry) Stop(id string) error {
	r.mu.Lock()
	e, ok := r.sessions[id]
	if ok {
		delete(r.sessions, id)
	}
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("session: no session %q", id)
	}
	e.cancel()
	<-e.done
	return nil
}

// Result returns a finished session's outcome, and false if it is still running.
func (r *Registry) Result(id string) (Result, bool) {
	r.mu.Lock()
	e, ok := r.sessions[id]
	r.mu.Unlock()
	if !ok {
		return Result{}, false
	}
	select {
	case <-e.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return e.result, true
	default:
		return Result{}, false
	}
}

// Ids lists the live sessions, oldest first.
func (r *Registry) Ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	// Ids are assigned in creation order, so sorting by the numeric suffix keeps
	// the listing stable. Ranging the map alone would not be.
	sortIds(ids)
	return ids
}

// Len is how many sessions are live.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// Evict stops every session that has not been touched within idle, or that has
// been alive longer than maxAge, and returns the ids it stopped. A server calls
// this on a timer: a viewer who closes the tab leaves a session running at one
// event per second forever otherwise.
func (r *Registry) Evict(idle, maxAge time.Duration) []string {
	now := r.now()

	r.mu.Lock()
	expired := make([]*entry, 0)
	for id, e := range r.sessions {
		if now.Sub(e.touched) >= idle || (maxAge > 0 && now.Sub(e.created) >= maxAge) {
			expired = append(expired, e)
			delete(r.sessions, id)
		}
	}
	r.mu.Unlock()

	stopped := make([]string, 0, len(expired))
	for _, e := range expired {
		e.cancel()
		<-e.done
		stopped = append(stopped, e.id)
	}
	sortIds(stopped)
	return stopped
}

// StopAll ends every session. Used on server shutdown.
func (r *Registry) StopAll() {
	r.mu.Lock()
	entries := make([]*entry, 0, len(r.sessions))
	for id, e := range r.sessions {
		entries = append(entries, e)
		delete(r.sessions, id)
	}
	r.mu.Unlock()

	for _, e := range entries {
		e.cancel()
		<-e.done
	}
}

func sortIds(ids []string) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && idNum(ids[j]) < idNum(ids[j-1]); j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

func idNum(id string) int {
	n := 0
	for i := 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return n
		}
		n = n*10 + int(id[i]-'0')
	}
	return n
}
