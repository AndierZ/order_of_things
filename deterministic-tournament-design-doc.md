# Deterministic Tournament Engine — Design & Requirements Doc

> **This is the design record written before the build, kept as written.** It is
> a snapshot of the thinking going in, not a description of what exists. Where
> the build diverged it diverged deliberately, and `README.md` is authoritative
> on what was actually built. The divergences worth knowing about:
>
> - **Random became Cooperator.** §2 and §5 list a Random strategy at tier 0.
>   The build uses an always-cooperate strategy instead: it occupies the same
>   tier — its decision reads nothing — without asking a reader to hold
>   pseudo-randomness and global ordering in their head at the same time. The
>   seeded-from-stream-position requirement in §3 still holds, and is satisfied
>   by the injector's pairing draw, which is a pure function of (seed, game id)
>   with no generator object anywhere.
> - **"Actor" became "Strategy."** §2–§5 use "actor" for the thing a game is
>   played between. That read as though it meant an individual replica, so the
>   code calls it a strategy; replica identity lives in `Header.SenderId`.
> - **More is persisted than §9 describes.** §9 says only the final outcome for a
>   seed is durable. The build persists the whole canonical run — every event and
>   a state checksum after each one — because a rejoining replica has to be
>   checked against the *entire* tournament, not just its ending. See the README
>   on why replaying only the log so far turned out not to be enough.
> - **§7 and §12 understate what v2 needs.** Both say v2 changes only the
>   injector and leaves the FSMs alone. That is not true of the build as it
>   stands: the store deliberately permits exactly one game in flight and panics
>   otherwise, and the strategies read a single `CurrentGame`. A real v2 needs a
>   multi-game store and per-strategy dependency views as well as a new admission
>   policy — the injector is the smallest part of it. v2 is out of scope; the
>   README describes what it would actually take.
> - **The state checksum is not a Merkle root.** §8 draws an analogy to state-root
>   verification in blockchain systems, which is apt as prior art but not as a
>   description of this implementation: the value here is an FNV-1a chain, good
>   for catching accidental divergence and useless against an adversary. The
>   threat model is bugs, not Byzantine replicas.

**Context:** Anthropic SWE take-home assignment. Theme 1 (Exploration & Understanding) blended with Theme 3 (Systems & Reliability). Target: a self-contained, deployable prototype that makes distributed-systems concepts — determinism, replicated state machines, fault tolerance, dependency-aware concurrency — viscerally understandable without requiring the reviewer to bring domain expertise.

---

## 1. What We're Building and Why

We are building an iterated multi-strategy Prisoner's Dilemma tournament (in the spirit of Nicky Case's "Evolution of Trust" and the recent Veritasium treatment of it), not as a game for its own sake, but as a vehicle for demonstrating a specific, non-obvious systems insight:

> **The amount of coordination a component needs is exactly determined by how much shared state its decision depends on.**

We rejected a more "realistic" domain (an order-processing / inventory / revenue pipeline) explicitly because it read as a generic CRUD system dressed up with distributed-systems vocabulary. The tournament domain does the same conceptual work but makes the coordination requirement legible from the strategy's name alone, which is a stronger teaching device and a more fun demo.

We are explicitly not trying to teach "how to build a resilient order-processing system." We are trying to make one specific idea unforgettable: determinism is not a single global property you either have or don't — it's something you can scope precisely, and the amount of coordination you pay for should match the amount of shared state you actually depend on, no more.

## 2. Core Thesis / The Correspondence Table

Four actor strategies map directly onto four tiers of coordination requirement:

| Strategy | Decision depends on | Coordination required |
|---|---|---|
| Random | nothing | none — fully parallel, no shared state at all |
| Flip-flop (or similar) | the actor's own last move | private per-actor state; no coordination with other actors |
| Tit-for-tat | this specific opponent's last move against this actor | coordination only within the (A, B) pair — a natural two-party shard |
| Copycat | the current global leader | global shared state — the one place real blocking is necessary |

This table (or its visual equivalent) should be the spine of the presentation. Everything else in this doc exists to make this table demonstrably true, not just asserted.

## 3. Domain Model

- N actors participate in repeated pairwise games.
- Each actor runs one of the four strategies above.
- Games are injected into a sequenced event stream. Each game gets a sequence number at admission time.
- Copycat's decision reads a shared, global scores/leaderboard state machine — gated by a watermark (see §7) — rather than its own private history.
- All "randomness" in the system (which pairs play, which strategy behaves stochastically) must be a deterministic function of a single injectable seed and stream position — never an independent, ungoverned source of entropy. This includes randomness *inside* a strategy's own logic (e.g., the Random strategy), not just the game-scheduling order — a strategy that calls its own ungoverned PRNG is a second, hidden source of nondeterminism that the seed doesn't actually control.

## 4. Invariants

These are the properties the system must provably satisfy. Everything else (architecture, milestones) exists in service of these.

1. **Deterministic logical output within an actor.** The same actor always makes the same decision given the same event history.
2. **Deterministic logical output of the system.** The game outcome is always the same given the same event history.
3. **State transition functions must be pure/deterministic.** No `time.Now()`, no true randomness, no dependence on Go map iteration order (randomized by design), and no dependence on `select` over multiple simultaneously-ready channels (also pseudo-random by design) — both are real, easy-to-miss sources of hidden nondeterminism in Go and should be treated as the same class of bug as calling `time.Now()`.
4. **The system interacts through a sequenced event stream; each event is delivered exactly once per independent replica, in a fixed order.** ("Exactly once" is scoped per-replica, not system-wide — active-active means each of an actor's two replicas legitimately receives every event once. "Fixed order" means: globally in v1, and the correctly-scoped partial order per actor/pair in v2 — never re-orderable, since exactly-once-but-reorderable delivery would still violate invariants 1–2.)
5. **The system should remain highly available.** This is a liveness property, independent of 1–4. A single-instance system with zero redundancy could satisfy invariants 1–4 perfectly while having no fault tolerance at all — availability is verified by killing something and checking the system still makes progress, not by comparing outputs.

**Named guardrail (not a numbered invariant, but load-bearing):** *watermark soundness.* Invariants 1 and 2 only hold for Copycat if the shared scores state it reads is scoped correctly — i.e., the watermark never advances past a point where some lower-sequence game hasn't actually resolved. If it advances early, two runs of the *identical* input log can produce different Copycat decisions purely from real-time race timing, which is invariant 1/2 breaking, just via a different mechanism than the "obvious" one (a mutated transition function). This is the specific failure mode the unsafe/safe Copycat demo (§9) is built to expose.

**Also worth stating explicitly, since it's easy to under-scope:** "global ordering" is not actually required by invariants 1–2. What's required is that *for each actor's own decision*, there exists a fixed, well-defined order over the events that decision depends on — not necessarily the same order any other actor sees, and not necessarily the full admission order. v1 satisfies this trivially because global order implies every sub-order. v2's entire job is constructing the correct, narrower order per actor/pair without relying on a global one — v2 does not relax invariant 1, it satisfies it via a smaller, deliberately-constructed dependency scope.

## 5. Architecture & Components

Core pipeline, in dependency order:

1. **Sequencer** — deliberately dumb. A single admission gate. Assigns sequence numbers, delivers events. Has no knowledge of game/actor semantics. Not made highly available itself (see §11 — a real system would back this with something like Raft; explicitly out of scope here).
2. **Sequencer client** — the interface components use to talk to the sequencer.
3. **Component event loop** — built on the sequencer client; the common substrate every component (actor, tracker) runs on.
4. **Game Injector** — owns the *admission policy*: when is it safe to inject the next game into the stream. This is where v1 and v2 actually differ (see §6–7); the sequencer itself does not change between versions.
5. **Actors and their FSMs** — one FSM per strategy type (Random, Flip-flop, Tit-for-tat, Copycat). Each actor is implemented as an **active-active pair of replicas** (see §8), not a single instance — this is the same RSM pattern applied fractally at the level of an individual actor, not just the system as a whole.
6. **Game result tracker and its FSM** — the shared scores/leaderboard state machine. Must apply score-affecting outcomes strictly in sequence order, buffering any out-of-order arrival, and must expose a query like "score as of sequence N" that blocks until every delta below N has actually been applied. This blocking-until-contiguous query *is* the watermark mechanism — there is no separately named "watermark component"; it's a property of how the tracker is queried.
7. **UI** — Nicky Case–inspired visual presentation of the live v1 tournament (see §14).

Additional components identified during design review, needed but not obviously implied by the above:

- **Active-active replica orchestration** — spinning up an actor's replica pair, racing them, arbitrating first-response-wins, discarding the loser. Distinct from the FSM logic itself (see §8).
- **Boot-time replay + quarantine, combined with a determinism-bug injection hook** — the mechanism that lets a restarting replica replay from the log, compute a state hash, compare it against the live replica, and refuse to rejoin on mismatch — plus an explicit toggle to make one replica "buggy" on demand (e.g. skip the watermark check) so the divergence/quarantine path can actually be triggered for the demo rather than only existing in theory (see §10).
- **Golden-outcome persistence** — since the rest of the system is in-memory only per session, a small durable store of "for seed X, the canonical final outcome is Y" is needed to make replay/divergence checks meaningful across process restarts (see §9).

## 6. V1 Design

**Admission policy:** the Game Injector runs an FSM that only allows a new game onto the stream once the previous game has fully resolved. There is never more than one game in flight globally.

This makes global total ordering trivial by construction — there is no dependency graph to reason about, because there is never more than one thing happening at once. This is not a weakness of v1; it's the deliberate ground-truth baseline that v2 has to match. If a component (e.g. Copycat) is injected with an event at a time a previous game is still "in progress," under v1 rules that's actually impossible — that scenario is explicitly deferred to v2.

## 7. V2 Design

**The sequencer does not change.** The change is entirely in the Game Injector's admission FSM: it now tracks each actor's busy/free status and admits any new game whose both participants are currently free. Disjoint pairs (e.g. A-vs-B and C-vs-D) can now run fully concurrently.

This one rule is sufficient to give both "local" strategy types a clean serialized view for free, without the injector needing to know anything about strategy semantics:
- An actor is never in two games at once → Flip-flop's own-history view is automatically serialized.
- An actor is never in two games at once → Tit-for-tat's pairwise-history view with any specific opponent is automatically serialized too.

**What per-actor mutual exclusion does *not* solve: Copycat.** Two fully disjoint, concurrently-running pairs can both be writing into the shared scores tracker at the same time. This is where the sequence-number/watermark mechanism (§5, item 6) is actually exercised for the first time — v1 satisfies it trivially (only one game ever in flight), v2 is where it either holds or doesn't.

**How disjoint concurrency stays safe for Copycat without extra machinery:** every game still carries a sequence number assigned at admission time. Even if a physically-faster disjoint game (say C-vs-D, seq 8) finishes before an earlier one (A-vs-B, seq 7), Copycat's read at, say, seq 10 blocks on the tracker until seq 7–9 have all been applied in order — regardless of real-time completion order. No deadlock is possible: a game at sequence N can only ever be blocked on strictly lower sequence numbers, which are either already resolved or actively in progress — never on something that itself depends on N.

**Logical order is fixed at admission, not completion.** This is the critical invariant for the v1/v2 correctness comparison to mean anything: a game's position in the logical sequence is determined the moment it's admitted, never by which physically finishes computing first. Otherwise the "same seed → identical outcome" claim would be false by construction.

**Open question to resolve during build, not before:** if "actor free" is driven by real-time completion signals, the interleaving *admission* order of otherwise-unrelated concurrent games could vary run to run from OS scheduling noise, even though each individual game's outcome stays deterministic. Whether this matters depends on whether anything downstream is sensitive to interleaving order versus just eventual completion — worth being deliberate about when implementing the v2 injector, not resolved here.

**Validation, in two parts (not one):**
1. **Correctness diff (precondition):** same seed, v1 vs v2 → byte-identical final tournament outcome. This has to pass before the speed comparison means anything.
2. **Benchmark:** once correctness is established, measure and report actual throughput/speedup of v2 over v1 for the same workload.

V2 is not integrated into the interactive UI — a UI trying to visually narrate microsecond-scale concurrency would either have to fake-slow-it-down (misleading) or be illegible. V2 is demonstrated as a benchmark/test harness instead, which is a more honest way to show a genuine speed claim.

## 8. Active-Active Fault Tolerance & Quarantine

Each actor is implemented as **two replicas** running the identical deterministic FSM. The sequencer-gated arbitration is simple: both replicas compute independently, whichever response the sequencer receives first wins and is committed to the log; the second (losing) response is discarded, not compared.

**Why active-active here, when it's usually hard:** active-active is typically difficult for general business logic because you can't safely re-run arbitrary side-effecting code twice and reconcile the results. It becomes trivial here specifically *because* determinism (invariants 1–3) is already guaranteed — there's nothing to reconcile, since both replicas are guaranteed to compute the same answer absent a bug. This should be stated explicitly in the write-up: it is not a shortcut taken to make the demo easier, it is itself a demonstration of what determinism buys you.

**Why we don't compare the loser against the winner as a live correctness signal (explicit, deliberate scope decision):** trivially cheap to detect if the second-place response differs from the winner, but this does not establish correctness — it only detects *divergence*, and the replica that happened to win the race could just as easily be the buggy one. Going further ("determine correctness on the fly") is a much harder, out-of-scope problem. Note also the deeper limit: even if both replicas *agree*, that isn't proof of correctness either — if the bug is in the shared deterministic core itself (not a replica-specific nondeterminism), both replicas will compute the same wrong answer and agree with each other while being wrong. Active-active protects against *replica-specific* nondeterminism (a bad deploy on one instance, environment drift, a stray `time.Now()`), not against bugs baked into the shared logic. This limitation should be named explicitly in the write-up, not glossed over.

**Real-world trade-off worth naming (not solving) in the write-up:** in production systems facing this same problem, the common choice is to quarantine the losing/restarting instance anyway to preserve a single unambiguous event stream — accepting that this could still contain an undetected correctness issue requiring later manual reconciliation. We are naming this trade-off but not attempting to solve "correctness reconciliation" in this project.

**Boot-time replay + quarantine mechanism:** a restarting replica replays from the event log, computes a state hash at each point, and compares it against the currently-live replica's state hash at the same point. On mismatch, the restarting replica is refused rejoin (quarantined) rather than allowed to serve as an active member of the pair. This mirrors a real pattern used in some financial/blockchain replicated systems (state-root verification before a node is trusted) — worth naming as such in the write-up, since it signals the mechanism is grounded in something real rather than invented for the demo.

**Determinism-bug injection hook:** an explicit toggle to make one replica "buggy" on restart (e.g., skip the watermark check, or otherwise break purity) so the divergence → quarantine path can be triggered on demand during the demo, rather than only existing as an untested code path.

## 9. Golden Outcome Persistence

Each user session spins up its own fully in-memory instance of the entire system (sequencer, actors, tracker) — nothing about a session persists by default. Per-game event logs do **not** need to be durable.

What *is* persisted: the final **outcome** for a given seed (not the full log) — a small canonical record of "for seed X, the correct final result is Y." This is what makes replay/divergence validation meaningful even though everything else is ephemeral: it lets a fresh session (or a restarting replica within a session) validate against a durable reference rather than needing a live paired replica to already exist within that exact ephemeral process.

## 10. What's Explicitly Discussed But Not Built

These were identified as real, important extensions of the core idea during design, and are called out deliberately in the write-up/video — but are out of scope for the actual build, by explicit decision:

- **Multiple external gateway APIs (nondeterministic arrival — when and what enters the system).** The sequencer already generalizes to N uncontrolled producers racing to the same admission gate for free; adding a second real endpoint doesn't change the core mechanism. Built as a single entry point in the demo, described in the write-up as generalizing to N.
- **Mid-execution external calls with idempotent replay.** A genuinely different, harder problem from arrival-time nondeterminism: a component consulting an external, non-idempotent, nondeterministic service *during* a state transition. The real fix — recording "call issued" and "call resolved" as two separate durable log facts, so replay only ever re-derives from what's logged rather than re-executing the actual side effect — is exactly the class of bug that causes real-world double-charges and duplicate side effects in production event-sourced systems. Discussed in the write-up/video only; no dedicated code.

## 11. Explicit Trade-offs / Non-Goals

Stated deliberately so they read as scoping decisions, not oversights:

- **Single process, in-process concurrency (goroutines), not distributed services across pods/hosts.** A real system would run these as separate services with a highly-available sequencer (e.g., Raft-backed). This adds real value for demonstrating network-partition tolerance and CAP-theorem trade-offs, but not for the specific concepts this project is teaching — named as a non-goal, not a gap.
- **Active-active over active-passive/leader election** — see §8's rationale (determinism makes it trivial; itself a demonstration of the point).
- **Divergence detection only; "correctness on the fly" is out of scope** — see §8.
- **Fault injection is simulated, not a real process kill.** Consistent with the single-process design — there's no OS-level process to `SIGKILL`; "killing" a replica means stopping its loop / making it stop responding.
- **In-memory only, per user session** — see §9 for what's persisted instead and why.
- **Pacing controls (pause / speed up / slow down)** exposed in the UI — both for demo legibility and doubling as a tool for stepping through mechanics deliberately during the recorded video walkthrough.

## 12. Milestones & Build Sequence

Core components (in dependency order):

1. Sequencer
2. Sequencer client
3. Component event loop (built on the sequencer client)
4. Game Injector (v1 policy: single game in flight; v2 policy: per-actor busy/free tracking — see §6–7)
5. Each actor and its FSM (Random, Flip-flop, Tit-for-tat, Copycat), including deterministic-from-stream-position randomness inside strategies that need it
6. Game result tracker and its FSM (sequence-ordered application + blocking watermark query)
7. Active-active replica orchestration (spin-up, race, arbitrate first-response-wins) — pulled out as distinct from the FSM logic itself
8. Boot-time replay + state-hash quarantine, combined with the determinism-bug injection hook
9. Golden-outcome persistence
10. UI (interactive v1 demo)

V2 additions:

1. V2 Game Injector admission policy (per-actor busy/free tracking, enabling concurrent disjoint-pair games) — the sequencer and actor FSMs themselves do not need to change
2. Correctness diff: same seed, v1 vs v2 → identical final outcome (precondition)
3. Benchmark harness showing v2's throughput vs. v1 (only meaningful once #2 passes)

**Time-boxed execution order:**
1. **30 min** — Sequencer backend platform
2. **30 min** — V1 backend (game injection, game tracking, actor FSMs, active-active + replay/quarantine/bug-hook, golden-outcome persistence)
3. **60 min** — UI implementation and hosting
4. **Stretch, 30 min** — V2 backend (dependency-aware injector, correctness diff, benchmark)

All v1 items are must-have. V2 is the stretch goal. The UI is treated as the actual risk in this plan — the backend mechanisms (sequencer, FSMs, active-active) are well understood going in; a legible, engaging, Nicky-Case-quality visual is the harder unknown.

## 13. Presentation / Demo Beats

Suggested sequence for the recorded walkthrough, each beat tied to a specific invariant or design decision above:

1. **v1 baseline** — single global game in flight, trivially-ordered, deterministic outcome for a given seed.
2. **v1 → v2 correctness + speed** — same seed, identical final outcome (the diff), then the throughput number (the benchmark). Correctness first, speed second — the speed number is meaningless without the preceding diff.
3. **Active-active in action** — both replicas racing per game, one wins, one is discarded.
4. **Fault + recovery** — kill a replica mid-game, restart, replay-and-validate against the golden outcome, rejoin.
5. **Determinism bug → quarantine** — inject the bug via the hook, restart the buggy replica, show its replayed state hash diverging from the live replica's, show it refused rejoin. This is the payoff of invariants 3–4 and the whole reason active-active is safe to do at all.
6. **Copycat: unsafe vs. safe** — the unsafe variant is a one-line difference (reads the tracker's raw current value instead of the watermark-gated blocking query). Artificially slow down one game to visibly show every higher-sequence Copycat decision blocked and cascading once the slow game resolves — makes the cost (and necessity) of the global dependency visible rather than asserted.

Visual style: actors as nodes, games as edges/interactions between them, a live leaderboard panel, and a visible watermark indicator (provisional vs. final) — in the spirit of Nicky Case's "Evolution of Trust."

## 14. Open Questions to Resolve During Build (not before)

- Whether "actor free" in the v2 injector should be driven by real-time completion signals (and if so, whether that introduces run-to-run variance in *interleaving* of unrelated concurrent games — separate from, and not a violation of, per-game outcome determinism).
- Exact hashing scheme for replica state comparison (what's included, granularity/cadence of hashing).
- Exact mechanism for deriving in-strategy randomness (e.g. Random strategy's coin flip) from stream position rather than an independent PRNG call.
