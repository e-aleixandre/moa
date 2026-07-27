import { TriangleAlert, AlertOctagon } from "lucide-preact";
import { Kbd, Button, Chip } from "../../primitives/index.js";
import "./PermissionCard.css";

// buildCommandFragments — locates every occurrence of dangerTokens in
// `command` and returns an array of fragments (plain string or
// { danger: token }) to render without recursion. Overlaps are
// resolved by preferring the longest match at the same start position;
// empty/non-string tokens are ignored.
function buildCommandFragments(command, dangerTokens = []) {
  const tokens = (dangerTokens || []).filter(
    (t) => typeof t === "string" && t.length > 0
  );
  if (!tokens.length) return [command];

  // Collects all (start, end) matches for all tokens.
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

  // Sort by start asc, and on tie by length desc (longest match wins).
  matches.sort((a, b) => a.start - b.start || b.end - a.end - (a.end - a.start));

  // Select non-overlapping matches, left to right, preferring the
  // longest one when competing for the same start position.
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

// PermissionCard is a presentational-only mock used by the galleries and by
// the real PermissionPrompt container (see ./PermissionPrompt.jsx). The extra
// props below (disabled, error, onFeedbackToggle/feedbackActive,
// onRuleToggle/ruleActive, children) are all OPTIONAL and only used by the
// real container — omitting them (as every gallery demo does) reproduces the
// exact previous markup/behavior.
export function PermissionCard({
  title,
  command,
  dangerTokens,
  scope = [],
  variant = "normal",
  alwaysLabel,
  timer,
  disabled = false,
  error,
  onAllow,
  onAlways,
  onDeny,
  onFeedbackToggle,
  feedbackActive = false,
  onRuleToggle,
  ruleActive = false,
  children,
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
      {error && <div class="perm-error">{error}</div>}
      <div class="perm-actions">
        <Button
          variant={destructive ? "danger-solid" : "success"}
          size="sm"
          disabled={disabled}
          onClick={onAllow}
        >
          {destructive ? "Allow anyway" : "Allow once"}
        </Button>
        {!destructive && alwaysLabel && (
          <Button variant="ghost" size="sm" className="btn-always" disabled={disabled} onClick={onAlways}>
            Always for <b>{alwaysLabel}</b>
          </Button>
        )}
        <Button variant="ghost" size="sm" className="btn-deny" disabled={disabled} onClick={onDeny}>
          Deny
        </Button>
        {onRuleToggle && (
          <Button
            variant="ghost"
            size="sm"
            className="btn-rule"
            disabled={disabled}
            aria-pressed={ruleActive}
            onClick={onRuleToggle}
          >
            Add rule
          </Button>
        )}
        {onFeedbackToggle && (
          <Button
            variant="ghost"
            size="sm"
            className="btn-feedback"
            disabled={disabled}
            aria-pressed={feedbackActive}
            onClick={onFeedbackToggle}
          >
            + feedback
          </Button>
        )}
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
      {children}
    </div>
  );
}
