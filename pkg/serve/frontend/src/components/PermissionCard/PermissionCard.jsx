import { Terminal } from "lucide-preact";
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

const isTextEntryTarget = (el) => {
  if (!el) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || el.isContentEditable;
};

// PermissionCard — the blocking "run this?" card, in the approval-card
// shape the owner picked: a glyph-and-title head, the command alone in its
// own sunken block with the working directory above it, and the decision
// at the bottom right as one solid and one ghost button. The command used
// to sit inline in a sentence ("Run `…`?") with the cwd lost among the
// scope chips; a command you are about to approve deserves its own line,
// and where it will run is the first thing to check.
//
// The ⏎ on the primary is a real key, not a painted one: with focus on the
// card (it is focusable, and a click inside gives it focus) Enter fires
// onAllow. Focus is never taken from the composer: an Enter meant to send a
// message must not approve a command.
//
// Production behaviour on top of the shape: Always, Add rule, + feedback,
// the error line, a destructive variant, danger tokens inside the command,
// and the optional scope/timer.
export function PermissionCard({
  title,
  command,
  cwd,
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
  const heading = title || (destructive ? "This command deletes things" : "Run this command?");

  const onKeyDown = (event) => {
    if (event.key !== "Enter" || disabled) return;
    const el = event.target;
    // A focused button already answers Enter natively; a text field owns it.
    if (isTextEntryTarget(el) || el?.tagName === "BUTTON") return;
    event.preventDefault();
    onAllow?.();
  };

  return (
    <div
      class={`zl-ask${destructive ? " is-danger" : ""}`}
      role="group"
      aria-label="Permission requested"
      tabIndex={-1}
      onKeyDown={onKeyDown}
      {...rest}
    >
      <div class="zl-ask-head">
        <span class="zl-ask-glyph" aria-hidden="true">
          <Terminal size={15} />
        </span>
        <span class="zl-ask-t">{heading}</span>
        {timer && <span class="zl-ask-timer zl-data">{timer}</span>}
      </div>
      <div class="zl-ask-cmd">
        {cwd && <div class="zl-ask-cwd zl-data">{cwd}</div>}
        <code class="zl-ask-cmd-text zl-data"><CommandLine command={command} dangerTokens={dangerTokens} /></code>
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
        {(onRuleToggle || onFeedbackToggle) && (
          <div class="zl-ask-aux">
            {onRuleToggle && (
              <button
                type="button"
                class="zl-ask-btn is-quiet"
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
                class="zl-ask-btn is-quiet"
                disabled={disabled}
                aria-pressed={feedbackActive}
                onClick={onFeedbackToggle}
              >
                + feedback
              </button>
            )}
          </div>
        )}
        {/* Allow first, then Always, then Deny: the order the tests fix and
            the one a keyboard reaches first. The reference puts the solid
            button last; the product keeps its own order and only moves the
            group to the right. */}
        <button
          type="button"
          class={`zl-ask-btn is-primary${destructive ? " is-danger" : ""}`}
          disabled={disabled}
          onClick={onAllow}
        >
          {allowLabel}
          <kbd class="zl-ask-key" aria-hidden="true">⏎</kbd>
        </button>
        {!destructive && alwaysLabel && (
          <button type="button" class="zl-ask-btn" disabled={disabled} onClick={onAlways}>
            Always for <b>{alwaysLabel}</b>
          </button>
        )}
        <button type="button" class="zl-ask-btn" disabled={disabled} onClick={onDeny}>
          Deny
        </button>
      </div>
      {children}
    </div>
  );
}
