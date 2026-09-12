import "./PermissionCard.css";

// buildCommandFragments — locates every occurrence of dangerTokens in
// `command` and returns an array of fragments (plain string or
// { danger: token }) to render without recursion. Overlaps are
// resolved by preferring the longest match at the same start position;
// empty/non-string tokens are ignored.
function buildCommandFragments(command, dangerTokens = []) {
  const text = command == null ? "" : String(command);
  const tokens = (dangerTokens || []).filter(
    (t) => typeof t === "string" && t.length > 0
  );
  if (!tokens.length) return [text];

  const matches = [];
  for (const token of tokens) {
    let from = 0;
    while (from <= text.length) {
      const idx = text.indexOf(token, from);
      if (idx === -1) break;
      matches.push({ start: idx, end: idx + token.length, token });
      from = idx + 1;
    }
  }
  if (!matches.length) return [text];

  matches.sort((a, b) => a.start - b.start || b.end - a.end - (a.end - a.start));

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
    if (m.start > pos) fragments.push(text.slice(pos, m.start));
    fragments.push({ danger: text.slice(m.start, m.end) });
    pos = m.end;
  }
  if (pos < text.length) fragments.push(text.slice(pos));
  return fragments;
}

function CommandLine({ command, dangerTokens = [] }) {
  const fragments = buildCommandFragments(command, dangerTokens);
  if (fragments.length === 1 && typeof fragments[0] === "string") return fragments[0];
  return fragments.map((frag, i) =>
    typeof frag === "string" ? frag : (
      <span key={i} class="danger">{frag.danger}</span>
    )
  );
}

function scopeLabel(chip) {
  return typeof chip === "object" ? chip.label : chip;
}

function scopeWarn(chip) {
  return typeof chip === "object" && chip.warn;
}

// PermissionCard — the blocking "run this?" card. Markup and CSS are the
// catalogue's (catalog/zones-lab.jsx ASK_CARD, zones-lab.css `.zl-ask*`),
// MOVED here rather than imitated. Raised, yellow-rimmed, the command in
// the sentence, Allow then Deny. The catalogue imports this component now.
//
// What is NOT the catalogue's is the production behavior plugged on top:
// Always, Add rule, + feedback, the error line, a destructive variant,
// danger tokens inside the command, and the optional scope/timer. The
// prototype had Allow/Deny; production still has to let you mean those
// extra things without changing what Allow and Deny do.
export function PermissionCard({
  title: _title,
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
  const allowLabel = destructive ? "Allow anyway" : alwaysLabel ? "Allow once" : "Allow";
  return (
    <div
      class={`zl-ask${destructive ? " is-danger" : ""}`}
      role="group"
      aria-label="Permission requested"
      {...rest}
    >
      <div class="zl-ask-t">
        Run <code class="zl-data"><CommandLine command={command} dangerTokens={dangerTokens} /></code>?
        {timer && <span class="zl-ask-timer zl-data">{timer}</span>}
      </div>
      {scope.length > 0 && (
        <div class="zl-ask-scope">
          {scope.map((chip, i) => (
            <span
              key={scopeLabel(chip) ?? i}
              class={`zl-ask-chip${scopeWarn(chip) ? " is-warn" : ""}`}
            >
              {scopeLabel(chip)}
            </span>
          ))}
        </div>
      )}
      {error && <div class="zl-ask-error">{error}</div>}
      <div class="zl-ask-acts">
        <button
          type="button"
          class={`zl-ask-btn is-primary${destructive ? " is-danger" : ""}`}
          disabled={disabled}
          onClick={onAllow}
        >
          {allowLabel}
        </button>
        {!destructive && alwaysLabel && (
          <button type="button" class="zl-ask-btn" disabled={disabled} onClick={onAlways}>
            Always for <b>{alwaysLabel}</b>
          </button>
        )}
        <button type="button" class="zl-ask-btn" disabled={disabled} onClick={onDeny}>
          Deny
        </button>
        {onRuleToggle && (
          <button
            type="button"
            class="zl-ask-btn"
            disabled={disabled}
            aria-pressed={ruleActive}
            onClick={onRuleToggle}
          >
            Add rule
          </button>
        )}
        {onFeedbackToggle && (
          <button
            type="button"
            class="zl-ask-btn"
            disabled={disabled}
            aria-pressed={feedbackActive}
            onClick={onFeedbackToggle}
          >
            + feedback
          </button>
        )}
      </div>
      {children}
    </div>
  );
}
