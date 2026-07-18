import { useState } from "preact/hooks";
import { ChevronDown } from "lucide-preact";
import "./BackgroundJob.css";

// BackgroundJob — strip de trabajo en background (bash async) dentro del
// stream de conversación: vive como pieza de contenido entre párrafos del
// documento del asistente, igual que FanoutBlock/ActivityLedger — de ahí que
// esté en components/ y no en layout/.
//
// El "peek" despliega un logtail mono con las últimas líneas; el estado
// abierto/cerrado es local (useState) y se expone con aria-expanded en el
// botón, igual que el patrón de LedgerRow (ActivityLedger) para filas
// colapsables. Todo vive bajo un único root `.bg-job` (strip + logtail), en
// vez de dos hermanos sueltos, para mantener el CSS co-locado bajo una sola
// raíz de componente.
export function BackgroundJob({
  jobLabel = "BG · JOB",
  cmd,
  progress,
  elapsed,
  lines = [],
  defaultOpen = false,
}) {
  const [open, setOpen] = useState(defaultOpen);
  const lastIdx = lines.length - 1;

  return (
    <div class="bg-job">
      <div class="bgjob">
        <span class="tag">{jobLabel}</span>
        <span class="cmd">
          {cmd}
          {progress && <span class="tail"> · {progress}</span>}
        </span>
        {elapsed && <span class="elapsed">{elapsed}</span>}
        <button
          type="button"
          class="peek"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          peek <ChevronDown size={12} class={`peek-caret${open ? " up" : ""}`} aria-hidden="true" />
        </button>
      </div>

      {open && lines.length > 0 && (
        <div class="logtail" role="log" aria-live="polite" aria-relevant="additions text">
          {lines.map((line, i) => {
            const isLast = i === lastIdx;
            const tone = typeof line === "string" ? undefined : line.tone;
            const text = typeof line === "string" ? line : line.text;
            return (
              <div key={line.id ?? i} class={`ln${tone ? ` t-${tone}` : ""}`}>
                {text}
                {isLast && <span class="ln-cursor" aria-hidden="true" />}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
