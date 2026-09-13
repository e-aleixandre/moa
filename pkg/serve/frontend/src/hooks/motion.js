// motion.js — the motion language's numbers, for code that has to WAIT.
//
// The language itself lives in tokens/tokens.css (the --motion-* block, with
// the reasoning). CSS drives every visible transition; this file exists because
// a few places need the same numbers in JavaScript: a presence hook that keeps
// a closing surface mounted until its exit has played, a swipe hook that writes
// an inline settle transition. Those used to carry their own copies (260, 220,
// 200, 160ms and three curves between four hooks), and a copy is a number that
// drifts. tokens/motion.test.js asserts these match the stylesheet.

export const MOTION = {
  ease: "cubic-bezier(0.32, 0.72, 0, 1)",
  easeExit: "cubic-bezier(0.4, 0, 1, 1)",
  hover: 120,
  fast: 180,
  base: 260,
  exitFast: 140,
  exitBase: 200,
};

export function prefersReducedMotion() {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}
