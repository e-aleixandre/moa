// hook-runtime.js — the one preact/hooks stand-in the vnode-walking tests
// share. mock.module is process-wide, so two files each mocking it would
// overwrite one another; instead both install this object and, before each
// test, say how useState behaves (`runtime.useState`). Effects never run unless a test sets `runtime.useEffect`.
const realHooks = await import("preact/hooks");

export const runtime = {
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useRef(initial) { return { current: initial }; },
  useEffect() {},
};

export const hooks = {
  ...realHooks,
  useState: (initial) => runtime.useState(initial),
  useRef: (initial) => runtime.useRef(initial),
  useEffect: (f, d) => runtime.useEffect(f, d),
  useLayoutEffect() {},
  useCallback(cb) { return cb; },
  useMemo(f) { return f(); },
};
