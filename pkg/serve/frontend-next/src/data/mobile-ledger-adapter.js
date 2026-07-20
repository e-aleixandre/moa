// mobile-ledger-adapter.js — PURE adapter from a projectStream `ledger` block
// (the same projection the desktop consumes, 5B) to the props MobileLedger
// expects. This is the ONLY data divergence between the desktop and mobile
// streams (decision: OPTION B — reuse projectStream, remap only the ledger).
//
// No preact, no JSX, no DOM: it takes a plain ledger block (and, optionally, the
// `diff` sibling block projectStream emits right after an edit row) and returns
// a serializable props object. Icons are returned as string KEYS (`iconKeys`)
// rather than JSX nodes so the adapter stays pure; the MobileStream component
// maps those keys to lucide icons before handing them to MobileLedger.
//
// ── INPUT (projectStream row shape) ─────────────────────────────────────────
//   ledgerBlock: { type:'ledger', id, rows:[ { tool, arg:{text}, out, status,
//                  id, body? } ] }
//   siblingDiff (optional): { type:'diff', filename, diffText, startLine }
//     projectStream emits an edit's real unified diff as a SIBLING block right
//     after the ledger, closing it. So a ledger has AT MOST one edit-with-diff
//     and it is always its LAST edit row. We FUSE that diff back into the row
//     (detail.type:'diff') so the mobile 3-level ledger shows the change inline,
//     and the caller must NOT render the diff a second time. (Fusion route,
//     chosen over rendering the diff standalone, to keep the mobile stream to
//     one touch surface per activity batch — see MobileStream.)
//
// ── OUTPUT (MobileLedger props) ─────────────────────────────────────────────
//   { summary, iconKeys, rows:[{ id, kind, name, action, result, detail }],
//     defaultOpen, defaultOpenRowIds, liveRow }
//   liveRow (nullable): { id, tool, arg, startedAt } — the tool call currently
//     in flight (stream-model.js's `live:true` row), pulled out of `rows` and
//     `summarize()` so MobileLedger can render it as its own always-visible
//     "B·Tail" live line (verb + object + caret + elapsed) instead of a normal
//     L2 row.
//   detail is one of:
//     { type:'diff', diffText, filename }   — an edit with a real unified diff.
//     { type:'bash', output }               — any row carrying a text body
//       (bash output, or a read/tool result preview). NOTE: read multi-file
//       calls degrade to a single bash-style output panel with the row's `body`
//       (the result preview). A fine per-file breakdown ({type:'files'}) is NOT
//       available from the projection — projectStream collapses a read into one
//       row with a text body — so we do NOT invent file entries. // TODO(5x):
//       richer read detail needs projectStream to carry per-file spans; not
//       touched here (out of scope, would change the shared projection).
//     null                                  — nothing to expand (row inert).

// Tool → coarse kind. MobileLedger doesn't branch on `kind` visually, but it is
// part of its row contract and documents the row's nature; unknown tools fall
// through to their lowercased token so the value stays honest.
const READ_TOOLS = new Set(['read', 'ls', 'grep', 'find']);
const EDIT_TOOLS = new Set(['edit', 'multiedit', 'write']);

export function mapToolToKind(tool) {
  const t = (tool || '').toLowerCase();
  if (EDIT_TOOLS.has(t)) return 'edit';
  if (t === 'bash') return 'bash';
  if (READ_TOOLS.has(t)) return 'read';
  return t || 'tool';
}

// resultString collapses a ledger row's (out,status) into the short result
// label MobileLedger renders. An ok row with no summary shows "ok" (which the
// component renders as a check); an error/rejected row prefers its own text.
export function resultString(out, status) {
  if (status === 'err') return out || 'error';
  if (status === 'warn') return out || 'rejected';
  return out ? out : 'ok';
}

// toolIconKey maps a tool to the icon-key MobileStream renders. Keys, not JSX,
// keep this module pure. 'tool' is the fallback (rendered as a wrench).
function toolIconKey(tool) {
  const t = (tool || '').toLowerCase();
  if (EDIT_TOOLS.has(t)) return 'pencil';
  if (t === 'bash') return 'terminal';
  if (t === 'read' || t === 'ls') return 'file';
  if (t === 'grep' || t === 'find') return 'search';
  return 'tool';
}

// deriveIconKeys returns the distinct icon keys for the tools in the ledger, in
// first-appearance order (deduped) — a compact glyph row for the L1 summary.
function deriveIconKeys(rows) {
  const keys = [];
  for (const r of rows) {
    const k = toolIconKey(r.tool);
    if (!keys.includes(k)) keys.push(k);
  }
  return keys;
}

const KIND_LABEL = { read: 'read', edit: 'edit', bash: 'bash' };

// summarize aggregates the ledger's rows into an honest one-line count grouped
// by kind, in first-appearance order: e.g. "2 reads · 1 edit · 1 bash".
function summarize(rows) {
  const order = [];
  const counts = {};
  for (const r of rows) {
    const k = mapToolToKind(r.tool);
    if (!(k in counts)) { counts[k] = 0; order.push(k); }
    counts[k]++;
  }
  return order
    .map((k) => {
      const n = counts[k];
      const label = KIND_LABEL[k] || k;
      return `${n} ${label}${n > 1 ? 's' : ''}`;
    })
    .join(' · ');
}

// buildDetail decides the L3 inline panel for a row. An edit with its fused
// unified diff → a diff panel; any other row carrying a text body → a bash-
// style output panel (this is where multi-file reads degrade — see the module
// header); a body-less row → null (inert, not expandable).
export function buildDetail(row, siblingDiff) {
  const kind = mapToolToKind(row.tool);
  if (siblingDiff && kind === 'edit') {
    return { type: 'diff', diffText: siblingDiff.diffText, filename: siblingDiff.filename || '' };
  }
  if (row.body) return { type: 'bash', output: row.body };
  return null;
}

// adaptLedger turns a projectStream ledger block (and its optional diff sibling)
// into MobileLedger props. The sibling diff is attached to the ledger's LAST
// edit row (projectStream guarantees it follows that row and closes the ledger).
//
// The LIVE row (last row with `live:true`, see stream-model.js#toLedgerRow) is
// pulled OUT of `rows`/`summarize()` and returned separately as `liveRow`, so
// MobileLedger renders it as the always-visible "B·Tail" live line instead of a
// normal L2 row.
export function adaptLedger(ledgerBlock, siblingDiff = null) {
  const allRows = ledgerBlock && Array.isArray(ledgerBlock.rows) ? ledgerBlock.rows : [];
  const isLive = allRows.length > 0 && allRows[allRows.length - 1].live === true;
  const liveRowRaw = isLive ? allRows[allRows.length - 1] : null;
  const inRows = isLive ? allRows.slice(0, -1) : allRows;

  let diffRowIndex = -1;
  if (siblingDiff) {
    for (let i = inRows.length - 1; i >= 0; i--) {
      if (mapToolToKind(inRows[i].tool) === 'edit') { diffRowIndex = i; break; }
    }
  }

  const rows = inRows.map((r, i) => {
    const argText = r.arg && typeof r.arg === 'object' ? r.arg.text : r.arg;
    return {
      id: r.id != null ? String(r.id) : `row-${i}`,
      kind: mapToolToKind(r.tool),
      name: r.tool,
      action: argText || '',
      result: resultString(r.out, r.status),
      status: r.status,
      detail: buildDetail(r, i === diffRowIndex ? siblingDiff : null),
    };
  });

  const defaultOpenRowIds = rows
    .filter((r) => r.detail && r.detail.type === 'diff')
    .map((r) => r.id);

  const liveRow = liveRowRaw
    ? {
        id: liveRowRaw.id != null ? String(liveRowRaw.id) : 'live',
        tool: liveRowRaw.tool,
        arg: liveRowRaw.arg,
        startedAt: liveRowRaw.startedAt || null,
        liveTail: liveRowRaw.liveTail || '',
      }
    : null;

  return {
    summary: summarize(inRows),
    iconKeys: deriveIconKeys(inRows),
    rows,
    defaultOpen: defaultOpenRowIds.length > 0,
    defaultOpenRowIds,
    liveRow,
  };
}
