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
  useEffect(() => subscribeSelected(selectorRef, valueRef, () => setRev((n) => n + 1)), []);
  return valueRef.current;
}

// subscribeSelected subscribes and then compares once, straight away. The
// subscription is made in an effect, after the render that read the value, and
// a store change in between reaches no listener. Measured on the phone: the
// first roster landed in that gap on a third of cold starts, and the app sat
// on "Loading sessions…" with sessionsLoaded already true until the 15 s poll.
export function subscribeSelected(selectorRef, valueRef, onChange) {
  const check = () => {
    const next = selectorRef.current(store.get());
    if (Object.is(valueRef.current, next)) return;
    valueRef.current = next;
    onChange();
  };
  const unsubscribe = store.subscribe(check);
  check();
  return unsubscribe;
}
