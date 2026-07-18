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

// TOOL_ICONS — mapa tool → icono lucide, usado por LedgerRow. `Wrench` es el
// fallback para tools no mapeadas explícitamente.
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

// LedgerRow — una fila del activity ledger. Clicable para expandir/colapsar
// un `body` mono con la salida de la tool. Estado interno por defecto
// (`defaultOpen`); puede controlarse pasando `open`/`onToggle`.
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

// ActivityLedger — contenedor de filas de tool calls colapsables.
// `rows` es un array de props para LedgerRow. El estado abierto/cerrado de
// cada fila vive en LedgerRow (useState) y se asocia a la fila vía `key`;
// usar el índice como key haría que insertar/reordenar filas reasignase el
// estado de expansión a la fila equivocada. El consumidor DEBERÍA pasar un
// `row.key`/`row.id` estable (p.ej. el id del tool call). Si no lo hace,
// derivamos una key best-effort del contenido — no es tan robusta como un id
// real, pero es mejor que el índice puro.
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

