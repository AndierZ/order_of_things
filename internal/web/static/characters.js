// Hand-drawn characters in the spirit of Nicky Case's "The Evolution of Trust":
// flat fills, one heavy outline weight, faces built from a few primitives. Each
// strategy gets its own silhouette so it stays recognisable at small sizes and
// without relying on colour alone.

const INK = "#241f31";

const SHAPES = {
  cooperator: { fill: "#7ed389", body: `<circle cx="32" cy="34" r="24"/>` },
  flipper: { fill: "#b39ddb", body: `<rect x="9" y="11" width="46" height="46" rx="9"/>` },
  retaliator: { fill: "#ffb26b", body: `<path d="M32 8 L57 52 Q59 58 52 58 L12 58 Q5 58 7 52 Z"/>` },
  // The one that watches whoever is winning, so it wears the crown rather than
  // being covered by it -- a badge over the head, clear of the face.
  "copy-leader": {
    fill: "#7fc7e8",
    body: `<circle cx="32" cy="36" r="22"/>`,
    badge: `<path d="M32 2 l3.2 6.8 7.3 0.9 -5.4 5.1 1.4 7.2 -6.5-3.6 -6.5 3.6 1.4-7.2 -5.4-5.1 7.3-0.9 Z"/>`,
    eyeY: 33,
    mouthY: 45,
  },
};

// Faces. The mouth encodes one thing and one thing only: the decision. A smile
// for cooperating, a jagged grin for cheating, a flat line for neither yet.
//
// Three expressions, no more. Whose turn it is and whether a replica is alive are
// different facts and are shown differently -- by the card highlight and by how
// solid the character is drawn -- so the mouth stays a reliable read of what was
// actually decided rather than a mix of several signals.
function face(state, decision, eyeY, mouthY) {
  if (state === "gone") {
    return `
      <path d="M20 ${eyeY - 3} l8 7 M28 ${eyeY - 3} l-8 7" stroke="${INK}" stroke-width="3" stroke-linecap="round" fill="none"/>
      <path d="M36 ${eyeY - 3} l8 7 M44 ${eyeY - 3} l-8 7" stroke="${INK}" stroke-width="3" stroke-linecap="round" fill="none"/>
      <path d="M22 ${mouthY + 3} h20" stroke="${INK}" stroke-width="2.8" stroke-linecap="round" fill="none"/>`;
  }
  return `
    <circle cx="25" cy="${eyeY}" r="3.6" fill="${INK}"/>
    <circle cx="43" cy="${eyeY}" r="3.6" fill="${INK}"/>
    ${mouth(decision, mouthY)}`;
}

function mouth(decision, y) {
  const stroke = `stroke="${INK}" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" fill="none"`;
  if (decision === "cooperate") return `<path d="M24 ${y} q8 8 16 0" ${stroke}/>`;
  if (decision === "cheat") return `<path d="M23 ${y + 2} l4.5 -4.5 l4.5 4.5 l4.5 -4.5 l4.5 4.5" ${stroke}/>`;
  return `<path d="M25 ${y + 2} h14" ${stroke}/>`;
}

// characterSvg renders one replica.
//
// state is the replica's situation:
//   idle      - alive, not in the current game (drawn translucent)
//   awake     - alive and playing this game
//   deciding  - alive, playing, and it is this strategy's turn
//   gone      - killed or quarantined
//
// decision is the player's last move: "cooperate", "cheat", or null for neither.
export function characterSvg(strategy, state, decision) {
  const shape = SHAPES[strategy] || SHAPES.cooperator;
  const fill = state === "gone" ? "#c8c2cc" : shape.fill;
  const badge = shape.badge
    ? `<g fill="${state === "gone" ? "#c8c2cc" : "#ffd66b"}" stroke="${INK}" stroke-width="2.6" stroke-linejoin="round">${shape.badge}</g>`
    : "";
  return `
    <svg viewBox="0 0 64 64" class="face face-${state}" aria-hidden="true">
      <g fill="${fill}" stroke="${INK}" stroke-width="3.5" stroke-linejoin="round">
        ${shape.body}
      </g>
      ${badge}
      ${face(state, decision, shape.eyeY || 31, shape.mouthY || 43)}
    </svg>`;
}
