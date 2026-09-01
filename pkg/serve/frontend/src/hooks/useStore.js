import { useEffect, useRef, useState } from "preact/hooks";
import { store } from "../data/store.js";

// useStore — subscribe to one slice of the app store. The component re-renders
// only when `selector(store.get())` is not Object.is-equal to the last value.
// Selectors that allocate a new object every call will re-render every time;
// return a stored reference (or a primitive) when the slice has not changed.
export function useStore(selector) {
  const selectorRef = useRef(selector);
  selectorRef.current = selector;

  const selected = selector(store.get());
  const valueRef = useRef(selected);
  if (!Object.is(valueRef.current, selected)) {
    valueRef.current = selected;
  }

  const [, setRev] = useState(0);
  useEffect(() => store.subscribe(() => {
    const next = selectorRef.current(store.get());
    if (Object.is(valueRef.current, next)) return;
    valueRef.current = next;
    setRev((n) => n + 1);
  }), []);
  return valueRef.current;
}
