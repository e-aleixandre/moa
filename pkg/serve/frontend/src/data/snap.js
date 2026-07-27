// snap.js — resize-handle snap points, extracted as a pure module so the ratio
// math can be unit-tested (see tileTree.test.js) independently of the DOM-bound
// ResizeHandle. Ported verbatim from the old SPA's TileTree.jsx SNAPS/
// snapToRatio (25/33/50/67/75 → fr ratios [1,3][1,2][1,1][2,1][3,1]).

export const SNAPS = [
  { pct: 0.25, ratio: [1, 3] },
  { pct: 0.333, ratio: [1, 2] },
  { pct: 0.5, ratio: [1, 1] },
  { pct: 0.667, ratio: [2, 1] },
  { pct: 0.75, ratio: [3, 1] },
];

// snapToRatio maps a drag position (fraction 0..1 of the split's length) to the
// nearest snap point's fr ratio. Defaults to the centre (50/50) if nothing is
// closer, matching the old behaviour.
export function snapToRatio(pct) {
  let best = SNAPS[2];
  let minDist = Infinity;
  for (const sp of SNAPS) {
    const d = Math.abs(pct - sp.pct);
    if (d < minDist) { minDist = d; best = sp; }
  }
  return best.ratio;
}
