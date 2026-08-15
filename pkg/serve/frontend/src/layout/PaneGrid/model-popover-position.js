const VIEWPORT_MARGIN = 8;
const POPOVER_GAP = 8;

const clamp = (value, min, max) => Math.min(max, Math.max(min, value));

// Keep the menu attached to its badge where there is room. When a short pane
// has no room below it, opening upward preserves the entire control instead of
// clipping it at the pane or viewport edge.
export function positionModelPopover(anchor, popover, viewport) {
  const maxLeft = Math.max(VIEWPORT_MARGIN, viewport.width - popover.width - VIEWPORT_MARGIN);
  const left = clamp(anchor.right - popover.width, VIEWPORT_MARGIN, maxLeft);
  const below = anchor.bottom + POPOVER_GAP;
  const above = anchor.top - POPOVER_GAP - popover.height;
  const maxTop = Math.max(VIEWPORT_MARGIN, viewport.height - popover.height - VIEWPORT_MARGIN);

  let top = below;
  if (below + popover.height > viewport.height - VIEWPORT_MARGIN && above >= VIEWPORT_MARGIN) {
    top = above;
  }

  return { left, top: clamp(top, VIEWPORT_MARGIN, maxTop) };
}
