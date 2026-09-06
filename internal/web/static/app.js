import { characterSvg } from "/static/characters.js";

// Everything on this page is built by folding an ordered feed of events from the
// backend. Nothing is polled, nothing is recomputed from a rendered view of the
// world: a game row appears because a new-game event arrived, a decision fills in
// because a decision event arrived, the leaderboard moves because a game
// completed. The browser is one more replica of the same state machine.
//
// The one exception is deliberate and kept visibly separate: which replicas are
// alive is a fact about the deployment, not about the tournament, so it arrives
// as its own kind of frame and is never mixed into the feed.

const STRATEGIES = ["cooperator", "flipper", "retaliator", "copy-leader"];
const REPLICAS = ["r0", "r1"];

const $ = id => document.getElementById(id);

const state = {
  id: null,
  seed: null,
  games: 0,
  nextIndex: 0,
  rows: new Map(),   // gameId -> <tr>
  current: null,     // { id, a, b, next }
  status: null,
  source: null,
};

// ------------------------------------------------------------------- welcome

$("cast").innerHTML = STRATEGIES.map(s => characterSvg(s, "awake")).join("");

$("play").addEventListener("click", async () => {
  const raw = $("seed").value.trim();
  const seed = /^\d+$/.test(raw) ? Number(raw) : 0;

  $("play").disabled = true;
  $("play").textContent = "Building the reference run…";
  try {
    const res = await fetch("/api/sessions", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ seed }),
    });
    if (!res.ok) throw new Error(await res.text());
    start(await res.json());
  } catch (err) {
    $("play").disabled = false;
    $("play").textContent = "Play";
    alert("Could not start a session: " + err.message);
  }
});

function start(session) {
  state.id = session.id;
  state.seed = session.seed;
  state.games = session.games;

  $("metaSeed").textContent = session.seed;
  $("metaGames").textContent = session.games;
  document.body.classList.add("playing");

  buildPlayers();
  connect();
  control("start");
}

// ------------------------------------------------------------------ controls

async function control(action) {
  await fetch(`/api/sessions/${state.id}/control`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ action }),
  });
}

async function replicaAction(component, replica, action, defect) {
  const res = await fetch(
    `/api/sessions/${state.id}/replicas/${component}/${replica}`,
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ action, defect: defect || "" }),
    },
  );
  if (res.status === 409) {
    const body = await res.json();
    banner(body.error, false);
  }
}

$("pause").addEventListener("click", () => {
  const running = state.status?.running ?? true;
  control(running ? "pause" : "resume");
});
$("step").addEventListener("click", () => control("step"));

// ------------------------------------------------------------------- players

// One box per player, two replicas inside it. Four players, each run as an
// active-active pair -- not eight independent characters. Each replica is still
// individually killable, because that is the whole point of there being two.
function buildPlayers() {
  for (const strategy of STRATEGIES) {
    $(`pair-${strategy}`).innerHTML = `
      <div class="pair">
        <div class="pair-head">
          <div class="name">${strategy}</div>
          <div class="sub">active-active pair</div>
        </div>
        <div class="pair-body">
          ${REPLICAS.map(replica => `
            <div class="replica" id="rep-${strategy}-${replica}">
              <div class="art" id="art-${strategy}-${replica}"></div>
              <div class="tag">${replica} &middot; <span id="wins-${strategy}-${replica}">0</span></div>
              <div class="state" id="state-${strategy}-${replica}"></div>
              <div class="buttons">
                <button data-act="kill" data-c="${strategy}" data-r="${replica}" title="Stop this replica; its partner carries the player alone">K</button>
                <button data-act="restart" data-c="${strategy}" data-r="${replica}" title="Bring it back: it replays the log and rejoins">R</button>
                <button data-act="restart-with-bug" data-c="${strategy}" data-r="${replica}" title="Bring it back with a corrupted decision function">B</button>
              </div>
            </div>`).join("")}
        </div>
      </div>`;
  }

  document.querySelector(".nodes").addEventListener("click", e => {
    const button = e.target.closest("button");
    if (!button) return;
    replicaAction(button.dataset.c, button.dataset.r, button.dataset.act, "clock");
  });
  paintPlayers();
}

// A replica's drawn state is the combination of two independent things: whether
// it is alive (deployment) and whether its strategy is in the current game
// (tournament). Alive-but-not-playing is translucent; alive-and-playing is solid.
function faceState(strategy, replica) {
  const live = state.status?.replicas?.find(
    r => r.component === strategy && r.replica === replica,
  );
  // Only an explicit kill or a quarantine is a death. Every replica also stops
  // when the tournament ends, and drawing that as eight corpses would say
  // something went wrong when nothing did.
  if (live && (live.killed || live.quarantined)) return "gone";
  if (!state.current) return "idle";
  if (state.current.a !== strategy && state.current.b !== strategy) return "idle";
  return state.current.next === strategy ? "deciding" : "awake";
}

function paintPlayers() {
  for (const strategy of STRATEGIES) {
    for (const replica of REPLICAS) {
      const drawn = faceState(strategy, replica);
      const art = $(`art-${strategy}-${replica}`);
      if (art.dataset.state !== drawn) {
        art.dataset.state = drawn;
        art.innerHTML = characterSvg(strategy, drawn);
      }
      const box = $(`rep-${strategy}-${replica}`);
      box.classList.toggle("gone", drawn === "gone");
      box.classList.toggle("deciding", drawn === "deciding");

      const info = state.status?.replicas?.find(
        r => r.component === strategy && r.replica === replica,
      );
      $(`state-${strategy}-${replica}`).textContent =
        info?.quarantined ? "quarantined" : info?.killed ? "killed" : "";
      $(`wins-${strategy}-${replica}`).textContent = info?.wins ?? 0;
    }
  }
}

// -------------------------------------------------------------------- stream

function connect() {
  state.source?.close();
  const source = new EventSource(`/api/sessions/${state.id}/stream?from=${state.nextIndex}`);
  state.source = source;

  source.addEventListener("feed", e => {
    const event = JSON.parse(e.data);
    if (event.index < state.nextIndex) return; // already folded
    state.nextIndex = event.index + 1;
    apply(event);
    paintPlayers();
  });
  source.addEventListener("status", e => {
    state.status = JSON.parse(e.data);
    paintStatus();
    paintPlayers();
  });
  // Losing the connection is recoverable: reconnect and resume from where the
  // fold got to, exactly as a restarting replica resumes from the log.
  source.onerror = () => {
    source.close();
    setTimeout(connect, 800);
  };
}

// apply folds one event into the page. This is the whole renderer.
function apply(event) {
  logLine(event);
  pulse(event.seq);

  switch (event.kind) {
    case "new-game":
      state.current = { id: event.gameId, a: event.strategyA, b: event.strategyB, next: event.strategyA };
      addGameRow(event);
      break;

    case "decision":
      fillDecision(event);
      if (state.current && state.current.id === event.gameId) {
        state.current.next = state.current.next === state.current.a ? state.current.b : null;
      }
      break;

    case "game-completed":
      completeGame(event);
      paintBoard(event.leaderboard);
      state.current = null;
      break;
  }
}

// --------------------------------------------------------------------- games

function addGameRow(event) {
  const row = document.createElement("tr");
  row.className = "live fresh";
  row.innerHTML = `
    <td>${event.gameId + 1}</td>
    <td>${event.strategyA}</td><td class="pending" data-cell="a">&hellip;</td>
    <td>${event.strategyB}</td><td class="pending" data-cell="b">&hellip;</td>
    <td class="pending" data-cell="score">&hellip;</td>`;
  state.rows.set(event.gameId, row);
  $("games").prepend(row);
  setTimeout(() => row.classList.remove("fresh"), 400);

  // Keep the table from growing without bound over a long session.
  while ($("games").children.length > 60) $("games").lastElementChild.remove();
}

function fillDecision(event) {
  const row = state.rows.get(event.gameId);
  if (!row) return;
  const which = event.strategy === row.children[1].textContent ? "a" : "b";
  const cell = row.querySelector(`[data-cell="${which}"]`);
  cell.textContent = event.decision;
  cell.className = event.decision;
}

function completeGame(event) {
  const row = state.rows.get(event.gameId);
  if (!row) return;
  row.classList.remove("live");
  const cell = row.querySelector('[data-cell="score"]');
  cell.className = "delta";
  cell.textContent = `${signed(event.payoffA)} / ${signed(event.payoffB)}`;
}

const signed = n => (n > 0 ? `+${n}` : `${n}`);

// --------------------------------------------------------------- leaderboard

function paintBoard(leaderboard) {
  if (!leaderboard?.length) return;
  const top = Math.max(1, ...leaderboard.map(e => Math.abs(e.score)));
  $("board").innerHTML = leaderboard.map(entry => `
    <tr>
      <td>${entry.strategy}</td>
      <td class="bar"><div style="width:${Math.max(3, (Math.abs(entry.score) / top) * 100)}%"></div></td>
      <td class="score">${entry.score}</td>
    </tr>`).join("");
}

// ----------------------------------------------------------------- log + hud

function logLine(event) {
  const line = document.createElement("div");
  const detail =
    event.kind === "new-game"
      ? `game ${event.gameId + 1}: ${event.strategyA} vs ${event.strategyB}`
      : event.kind === "decision"
        ? `${event.strategy} ${event.decision}s`
        : `game ${event.gameId + 1} scored ${signed(event.payoffA)}/${signed(event.payoffB)}`;
  line.innerHTML = `
    <span class="s">${event.kind === "game-completed" ? "" : event.seq}</span>
    <span class="r">${event.replica ? `${event.component.slice(0, 4)}/${event.replica}` : "derived"}</span>
    <span>${detail}</span>`;
  $("log").prepend(line);
  while ($("log").children.length > 120) $("log").lastElementChild.remove();
}

let pulseTimer = null;
function pulse(seq) {
  $("seqNum").textContent = seq;
  const box = $("sequencer");
  box.classList.add("tick");
  clearTimeout(pulseTimer);
  pulseTimer = setTimeout(() => box.classList.remove("tick"), 220);
}

function paintStatus() {
  const s = state.status;
  if (!s) return;
  $("metaGame").textContent = s.completed;
  $("metaHash").textContent = s.stateHash;
  $("pause").textContent = s.running ? "Pause" : "Resume";
  $("pause").disabled = s.done;
  $("step").disabled = s.done;

  if (s.done) {
    banner(`Tournament complete — ${s.completed} games, state root ${s.stateHash}. Same seed, same result, every time.`, true);
  } else if (s.stalled) {
    banner(`Stalled: no live replica of ${s.waitingOn}. Nothing can happen until one comes back — restart either half.`, false);
  } else {
    $("banner").classList.remove("show");
  }
}

function banner(text, done) {
  const el = $("banner");
  el.textContent = text;
  el.classList.add("show");
  el.classList.toggle("done", Boolean(done));
}
