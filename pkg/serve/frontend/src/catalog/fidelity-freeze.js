// fidelity-freeze — makes the lab reproducible for the pixel harness.
//
// The fidelity comparator is only useful if a capture of an unchanged page is
// byte-identical to the previous one. Everything in the lab that reads the
// wall clock or a random source would otherwise move on its own: an elapsed
// counter ticks between two runs, a fixture says "12m" today and "13m" in a
// minute, the streaming prose lands on a different token. A comparator with
// that kind of noise is abandoned in a week, so the clock is frozen instead of
// the tolerance being raised.
//
// Active ONLY under ?view=scene. Every other view keeps the real clock: the
// lab is also where the owner looks at motion, and a frozen one would lie.

const FIXED_MS = Date.UTC(2026, 0, 15, 9, 41, 0); // 2026-01-15 09:41:00 UTC

export const FROZEN =
  typeof location !== "undefined" && new URLSearchParams(location.search).get("view") === "scene";

if (FROZEN) {
  // Date.now covers every "when" in the lab: the fixtures derive their ages
  // from it (specimen.js:10, catalog-backend.js:48) and the live zone takes
  // its t0 from it (zones-lab.jsx:1051). `new Date(x)` with an argument is
  // left alone, so serialised timestamps still parse.
  Date.now = () => FIXED_MS;

  // A deterministic stream in place of Math.random. The streaming prose picks
  // its burst sizes and gaps from it; fixtures may grow other uses. Same seed
  // every load, so the same sequence.
  let seed = 0x2f6d3c1b;
  Math.random = () => {
    // xorshift32: small, fast, and stable across engines.
    seed ^= seed << 13;
    seed ^= seed >>> 17;
    seed ^= seed << 5;
    seed >>>= 0;
    return seed / 0x100000000;
  };
}
