import { useState } from "preact/hooks";
import {
  FileText,
  Search,
  Terminal,
  Pencil,
  FilePlus,
  Globe,
  Database,
  ListTodo,
  Wrench,
} from "lucide-preact";
import "./ActivityLedger.css";

// TOOL_ICONS — tool → lucide icon map, used by LedgerRow. `Wrench` is the
// fallback for tools not explicitly mapped.
const TOOL_ICONS = {
  read: FileText,
  grep: Search,
  find: Search,
  bash: Terminal,
  edit: Pencil,
  multiedit: Pencil,
  write: FilePlus,
  fetch: Globe,
  fetch_content: Globe,
  db: Database,
  task: ListTodo,
  tasks: ListTodo,
};

function toolIcon(tool) {
  return TOOL_ICONS[tool] || Wrench;
}

// LedgerRow — a single activity ledger row. Clickable to expand/collapse
// a mono `body` with the tool's output. Internal state by default
// (`defaultOpen`); can be controlled by passing `open`/`onToggle`.
export function LedgerRow({
  tool,
  arg,
  out,
  status = "ok",
  body,
  defaultOpen = false,
  open,
  onToggle,
}) {
  const [innerOpen, setInnerOpen] = useState(defaultOpen);
  const isControlled = open !== undefined;
  const isOpen = isControlled ? open : innerOpen;
  const hasBody = body != null && body !== "";

  function toggle() {
    if (!hasBody) return;
    if (onToggle) onToggle(!isOpen);
    if (!isControlled) setInnerOpen((v) => !v);
  }

  const Icon = toolIcon(tool);
  const argText = typeof arg === "object" && arg !== null ? arg.text : arg;
  const argDetail = typeof arg === "object" && arg !== null ? arg.detail : null;

  return (
    <>
      <button
        type="button"
        class={`ledger-row${isOpen ? " open" : ""}`}
        onClick={toggle}
        aria-expanded={hasBody ? isOpen : undefined}
        disabled={!hasBody}
      >
        <span class="t-ic" aria-hidden="true">
          <Icon size={14} />
        </span>
        <span class="t-name">{tool}</span>
        <span class="t-arg">
          {argText}
          {argDetail && <b> · {argDetail}</b>}
        </span>
        {out && <span class={`t-out ${status}`}>{out}</span>}
      </button>
      {hasBody && isOpen && (
        <div class="ledger-body">{body}</div>
      )}
    </>
  );
}

// ActivityLedger — container for collapsible tool-call rows.
// `rows` is an array of props for LedgerRow. Each row's open/closed state
// lives in LedgerRow (useState) and is tied to the row via `key`;
// using the index as key would make inserting/reordering rows reassign
// the expansion state to the wrong row. The consumer SHOULD pass a
// stable `row.key`/`row.id` (e.g. the tool call's id). If it doesn't,
// we derive a best-effort key from the content — not as robust as a real
// id, but better than a plain index.
function rowKey(row, i) {
  if (row.key != null) return row.key;
  if (row.id != null) return row.id;
  const argText = typeof row.arg === "object" && row.arg !== null ? row.arg.text : row.arg;
  return `${row.tool ?? "row"}:${argText ?? ""}:${i}`;
}

export function ActivityLedger({ rows = [], children, className = "", ...rest }) {
  return (
    <div class={`ledger ${className}`.trim()} {...rest}>
      {rows.length > 0
        ? rows.map((row, i) => <LedgerRow key={rowKey(row, i)} {...row} />)
        : children}
    </div>
  );
}

