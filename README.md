# The Order of Things

**A debugger for distributed systems, built on replay.**

You cannot step through a distributed system. The interesting bugs live in the
interleaving — which goroutine won which race, which node was mid-restart when
the message arrived — and by the time you have a stack trace the interleaving is
gone. Attaching a debugger changes the timing you were trying to observe.

Replay is the way out, and it costs one thing: every component has to be a
deterministic state machine fed a single ordered log. Buy that, and a great deal
follows.

This is a working demonstration of what follows. It runs an iterated
Prisoner's Dilemma tournament — four strategies, each as an active-active pair of
replicas, nine components in all, every one on its own goroutine — and invites
you to break it while it runs.

## The claim

Kill a replica mid-game. Restart it. Bring one back subtly corrupted. **The
tournament ends exactly the same way**, and the corrupted replica never gets back
in.

That is not a fixed random seed doing the work. A seeded generator repeating
itself is a property of the generator, not of this system, which is why the seed
here is a constant and is not offered as a control. The claim is about what
survives the shocks *you* introduce, with no coordination between components
beyond the order of the log.

## What replay buys you

**Recovery.** A replica that dies rebuilds its entire state by replaying the log.
No snapshots, no state transfer, no catch-up protocol — it re-derives the past
from the same events everyone else saw.

**Verification before trust.** This is the part that makes it a debugger. A
recovering replica replays against a canonical chain of state roots, generated
before it ever ran, and is refused readmission if it diverges. Not "we noticed
later" — refused at the door, at the exact event where it first went wrong:

```
Eventloop quarantined: replayed state diverges from the canonical chain
for seed 20260906 at seq 2: state root d76b9… want 7ab00…
```

**Cheap redundancy.** Active-active is normally hard: you cannot re-run
side-effecting code twice and reconcile the results. It is trivial here precisely
*because* the components are deterministic — there is nothing to reconcile. Two
replicas of a player race for every decision; the sequencer admits the first and
drops the second as a duplicate.

## Two defects, two mechanisms

They are caught by different machinery, which is why both exist:

| defect | what it corrupts | caught by |
|---|---|---|
| `ImpureClock` | the **decision** function | comparing this replica's emissions against what the log recorded |
| `CorruptPayoff` | the **transition** function | the canonical state-root chain |

The first is invisible to the chain — a replica deciding badly still computes
state correctly. The second is invisible to the pair — a sibling running the same
defect agrees with it, which is the known limit of active-active. Only a
reference computed before either replica ran can say which one is wrong.

Both are injectable from the UI, and both name the actually-defective replica,
because on the replay path a replica is compared against recorded history it
cannot influence. On the *live* path that is not true: two replicas race, the
loser is quarantined, and it may well be the healthy half. Divergence detection
proves they disagreed — never which was right.

## The coordination table

The four strategies are not decoration. Each needs a different amount of the
world to make up its mind, and that is exactly how much coordination it costs:

| strategy | reads | coordination required |
|---|---|---|
| Cooperator | nothing | none |
| Flipper | its own last game | private state |
| Retaliator | its last game against *this* opponent | the (A, B) pair |
| CopyLeader | the global leaderboard | genuine global ordering |

In the code this is one function type, and what each implementation reads out of
the store is the whole of the difference between them (`internal/strategy`).

## Running it

```
go run ./cmd/server        # http://localhost:8080
go run ./cmd/tournament    # headless, verifies against the golden record
go test -race ./...
```

The page is driven entirely by folding an ordered feed of events. Nothing is
polled, nothing is recomputed from a rendered view: a game row appears because a
`new-game` event arrived, a decision fills in because a `decision` event arrived,
the leaderboard moves because a game completed. The browser is one more replica of
the same state machine, not a dashboard bolted onto it.

## Layout

```
internal/platform   sequencer, client, event loop, pacer — knows nothing of games
internal/fsm        the replicated state machine and its state-root chain
internal/injector   admission policy: when a game may start
internal/strategy   the four decision functions
internal/tracker    scores read model and the UI event feed
internal/golden     canonical outcomes and chains, persisted
internal/session    one system instance, plus supervision and fault injection
internal/web        HTTP, SSE, and the page
```

## Deliberate non-goals

Single process with goroutines rather than services across hosts; the sequencer
is not itself made highly available (a real one would be Raft-backed); faults are
simulated by stopping a loop rather than by signalling a process; only divergence
is detected, not correctness. Each is a scoping decision, argued in
`deterministic-tournament-design-doc.md`.

---

Inspired by [Nicky Case's *The Evolution of Trust*](https://ncase.me/trust/),
which this borrows its payoff matrix and its whole way of explaining things from.
