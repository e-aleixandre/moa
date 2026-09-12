const VIEWPORT_MARGIN = 8;
const POPOVER_GAP = 8;

const clamp = (value, min, max) => Math.min(max, Math.max(min, value));

// Keep the menu attached to its badge. The catalogue opens UPWARD and
// left-aligns to the button (`.zl-pop { left: 0; bottom: calc(100% + 8px) }`);
// production portals the same surface so a pane cannot clip it, and this
// function is the portal's version of that placement, clamped to the viewport.
export function positionModelPopover(anchor, popover, viewport) {
  const maxLeft = Math.max(VIEWPORT_MARGIN, viewport.width - popover.width - VIEWPORT_MARGIN);
  const left = clamp(anchor.left ?? (anchor.right - popover.width), VIEWPORT_MARGIN, maxLeft);
  const above = anchor.top - POPOVER_GAP - popover.height;
  const below = anchor.bottom + POPOVER_GAP;
  const maxTop = Math.max(VIEWPORT_MARGIN, viewport.height - popover.height - VIEWPORT_MARGIN);

  let top = above;
  if (above < VIEWPORT_MARGIN && below + popover.height <= viewport.height - VIEWPORT_MARGIN) {
    top = below;
  }

  return { left, top: clamp(top, VIEWPORT_MARGIN, maxTop) };
}
