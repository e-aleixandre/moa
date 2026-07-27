// tool-kind.js — PURE tool-name → coarse kind classification, shared by the
// ledger detail fusion (which tool rows get a diff vs an output panel) on both
// the desktop and mobile streams. No preact/DOM: safe to unit-test directly.
const EDIT_TOOLS = new Set(['edit', 'multiedit', 'write']);
const READ_TOOLS = new Set(['read', 'ls', 'grep', 'find']);

// mapToolToKind collapses a tool name to 'edit' | 'bash' | 'read' | its own
// lowercased token (unknown tools stay honest instead of a catch-all).
export function mapToolToKind(tool) {
  const t = (tool || '').toLowerCase();
  if (EDIT_TOOLS.has(t)) return 'edit';
  if (t === 'bash') return 'bash';
  if (READ_TOOLS.has(t)) return 'read';
  return t || 'tool';
}
