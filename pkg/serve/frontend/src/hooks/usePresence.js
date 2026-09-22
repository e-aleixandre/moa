import { useEffect, useRef, useState } from "preact/hooks";
import { MOTION, prefersReducedMotion } from "./motion.js";

// usePresence — keeps a surface mounted long enough to leave.
//
// Every floating surface in the product used to be `open && <Thing/>`: the
// enter played from a CSS @keyframes on mount, and the exit did not exist,
// because the node was gone the frame the state flipped. That asymmetry is
// half of what made the interface feel unfinished -- things slid in and then
// blinked out. Rule 2 of the motion language (tokens.css) says leaving is a
// movement too, just a shorter one.
//
//   const { mounted, leaving } = usePresence(open, MOTION.exitFast);
//   if (!mounted) return null;
//   <div class={`menu${leaving ? " is-leaving" : ""}`}>
//
// `leaving` is true for `exitMs` after `open` drops; the stylesheet answers
// with the exit keyframes on `.is-leaving`. A reopen during the exit cancels
// it. Under reduced motion the surface unmounts at once -- there is nothing
// to wait for.
//
// A timer rather than animationend: the event does not fire when the element
// is display:none, is clipped out of the compositor, or when a caller forgot
// the keyframes, and a surface that never unmounts is a worse bug than one
// that unmounts 20ms early.
export function usePresence(open, exitMs = MOTION.exitFast) {
  const [mounted, setMounted] = useState(open);
  const timer = useRef(null);

  useEffect(() => {
    if (open) {
      clearTimeout(timer.current);
      timer.current = null;
      setMounted(true);
      return undefined;
    }
    if (!mounted) return undefined;
    if (prefersReducedMotion()) {
      setMounted(false);
      return undefined;
    }
    timer.current = setTimeout(() => {
      timer.current = null;
      setMounted(false);
    }, exitMs);
    return () => clearTimeout(timer.current);
  }, [open, exitMs]);

  // Opening must render the surface in the same commit. Waiting for the effect
  // leaves first-open layout effects with no DOM node to measure; they do not
  // run again when only this hook's internal state catches up.
  const present = open || mounted;
  return { mounted: present, leaving: present && !open };
}

// usePresenceList — the same idea for a list whose items come and go on their
// own (toasts). Items removed from `items` stay in the returned list, flagged
// `leaving`, at the position they held, for `exitMs`; items present are passed
// through untouched.
export function usePresenceList(items, getKey, exitMs = MOTION.exitFast) {
  const [gone, setGone] = useState(() => new Map());
  const previous = useRef(items);
  const timers = useRef(new Map());

  useEffect(() => {
    const now = new Set(items.map(getKey));
    const dropped = [];
    previous.current.forEach((item, index) => {
      if (!now.has(getKey(item))) dropped.push({ item, index });
    });
    previous.current = items;
    if (dropped.length === 0 || prefersReducedMotion()) return;
    setGone((current) => {
      const next = new Map(current);
      for (const entry of dropped) next.set(getKey(entry.item), entry);
      return next;
    });
    for (const { item } of dropped) {
      const key = getKey(item);
      clearTimeout(timers.current.get(key));
      timers.current.set(key, setTimeout(() => {
        timers.current.delete(key);
        setGone((current) => {
          if (!current.has(key)) return current;
          const next = new Map(current);
          next.delete(key);
          return next;
        });
      }, exitMs));
    }
  }, [items]);

  useEffect(() => () => { for (const t of timers.current.values()) clearTimeout(t); }, []);

  const out = items.map((item) => ({ item, leaving: false }));
  if (gone.size === 0) return out;
  const live = new Set(items.map(getKey));
  for (const [key, { item, index }] of gone) {
    // An item that came back while leaving is simply present again.
    if (!live.has(key)) out.splice(Math.min(index, out.length), 0, { item, leaving: true });
  }
  return out;
}
