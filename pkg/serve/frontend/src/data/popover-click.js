// A repeated native click is the rest of a multi-click gesture, not a second
// decision to close what the first click just opened. Isolated pointer clicks
// have detail 1; keyboard clicks have detail 0 and keep normal toggle behavior.
export function nextPopoverOpenState(open, clickDetail = 0) {
  return clickDetail > 1 || !open;
}

export function setPopoverOpenFromClick(setOpen, event) {
  const clickDetail = event?.detail || 0;
  setOpen((open) => nextPopoverOpenState(open, clickDetail));
}
