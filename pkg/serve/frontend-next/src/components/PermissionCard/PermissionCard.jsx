import { TriangleAlert, AlertOctagon } from "lucide-preact";
import { Kbd, Button, Chip } from "../../primitives/index.js";
import "./PermissionCard.css";

// buildCommandFragments — localiza todas las apariciones de dangerTokens en
// `command` y devuelve un array de fragmentos (string normal o
// { danger: token }) para renderizar sin recursión. Solapamientos se
// resuelven prefiriendo el match más largo en la misma posición de inicio;
// tokens vacíos/no-string se ignoran.
function buildCommandFragments(command, dangerTokens = []) {
  const tokens = (dangerTokens || []).filter(
    (t) => typeof t === "string" && t.length > 0
  );
  if (!tokens.length) return [command];

  // Recolecta todos los matches (start, end) de todos los tokens.
  const matches = [];
  for (const token of tokens) {
    let from = 0;
    while (from <= command.length) {
      const idx = command.indexOf(token, from);
      if (idx === -1) break;
      matches.push({ start: idx, end: idx + token.length, token });
      from = idx + 1;
    }
  }
  if (!matches.length) return [command];

  // Ordena por inicio asc, y en empate por longitud desc (match más largo gana).
  matches.sort((a, b) => a.start - b.start || b.end - a.end - (a.end - a.start));

  // Selecciona matches no solapados, de izquierda a derecha, prefiriendo el
  // más largo cuando compiten por la misma posición de inicio.
  const selected = [];
  let cursor = 0;
  for (const m of matches) {
    if (m.start < cursor) continue;
    selected.push(m);
    cursor = m.end;
  }

  const fragments = [];
  let pos = 0;
  for (const m of selected) {
    if (m.start > pos) fragments.push(command.slice(pos, m.start));
    fragments.push({ danger: command.slice(m.start, m.end) });
    pos = m.end;
  }
  if (pos < command.length) fragments.push(command.slice(pos));
  return fragments;
}

function CommandLine({ command, dangerTokens = [] }) {
  const fragments = buildCommandFragments(command, dangerTokens);
  return (
    <>
      {fragments.map((frag, i) =>
        typeof frag === "string" ? (
          <span key={i}>{frag}</span>
        ) : (
          <span key={i} class="danger">
            {frag.danger}
          </span>
        )
      )}
    </>
  );
}

export function PermissionCard({
  title,
  command,
  dangerTokens,
  scope = [],
  variant = "normal",
  alwaysLabel,
  timer,
  onAllow,
  onAlways,
  onDeny,
  ...rest
}) {
  const destructive = variant === "destructive";
  return (
    <div class={`perm-card${destructive ? " danger" : ""}`} {...rest}>
      <div class="perm-head">
        <span class="p-icon" aria-hidden="true">
          {destructive ? <AlertOctagon size={15} /> : <TriangleAlert size={15} />}
        </span>
        <span class="p-t">{title}</span>
        {timer && <span class="p-timer">{timer}</span>}
      </div>
      {scope.length > 0 && (
        <div class="scope">
          {scope.map((chip, i) => {
            const label = typeof chip === "object" ? chip.label : chip;
            const warn = typeof chip === "object" && chip.warn;
            return (
              <Chip key={label ?? i} size="sm" mono tone={warn ? "warning" : undefined}>
                {label}
              </Chip>
            );
          })}
        </div>
      )}
      <div class="perm-cmd">
        <span class="dollar">$</span>{" "}
        <CommandLine command={command} dangerTokens={dangerTokens} />
      </div>
      <div class="perm-actions">
        <Button
          variant={destructive ? "danger-solid" : "success"}
          size="sm"
          onClick={onAllow}
        >
          {destructive ? "Allow anyway" : "Allow once"}
        </Button>
        {!destructive && alwaysLabel && (
          <Button variant="ghost" size="sm" className="btn-always" onClick={onAlways}>
            Always for <b>{alwaysLabel}</b>
          </Button>
        )}
        <Button variant="ghost" size="sm" className="btn-deny" onClick={onDeny}>
          Deny
        </Button>
        <span class="hint">
          {destructive ? (
            "no always for destructive ops"
          ) : (
            <>
              <Kbd>Y</Kbd> / <Kbd>N</Kbd>
            </>
          )}
        </span>
      </div>
    </div>
  );
}
