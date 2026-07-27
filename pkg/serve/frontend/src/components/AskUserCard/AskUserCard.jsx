import { useState, useRef, useCallback } from "preact/hooks";
import { Users, ArrowUp } from "lucide-preact";
import "./AskUserCard.css";

const isTextEntryTarget = (el) => {
  if (!el) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || el.isContentEditable;
};

// AskUserCard — agent question with numbered options (keyboard-first,
// 1/2/3) + free text. `options`: [{ label, recommended? }]. The free-text field
// is uncontrolled by default (keeps its own state — used by the gallery), but
// becomes controlled when `onFreeChange` is passed: then every keystroke is
// reported up so a stateful container (AskUserPrompt) can persist the answer
// per question and restore it when navigating back.
export function AskUserCard({
  question,
  options = [],
  onPick,
  onSubmitFree,
  freeValue,
  onFreeChange,
  placeholder = "Or answer in your own words…",
  ...rest
}) {
  const controlled = onFreeChange != null;
  const [freeInternal, setFreeInternal] = useState("");
  const free = controlled ? (freeValue || "") : freeInternal;
  const setFree = controlled ? onFreeChange : setFreeInternal;
  const rootRef = useRef(null);

  const submitFree = (event) => {
    event.preventDefault();
    const value = free.trim();
    if (!value) return;
    onSubmitFree?.(value);
    if (!controlled) setFreeInternal("");
  };

  const onKeyDown = useCallback(
    (event) => {
      if (isTextEntryTarget(event.target)) return;
      const num = Number(event.key);
      if (!Number.isInteger(num) || num < 1 || num > options.length) return;
      const idx = num - 1;
      event.preventDefault();
      onPick?.(options[idx], idx);
    },
    [options, onPick]
  );

  return (
    <div class="ask" ref={rootRef} onKeyDown={onKeyDown} {...rest}>
      <div class="ask-q">
        <div class="who">
          <Users size={13} aria-hidden="true" /> moa asks
        </div>
        <p>{question}</p>
      </div>
      <div class="ask-opts">
        {options.map((opt, i) => (
          <button
            key={opt.label ?? i}
            type="button"
            class="ask-opt"
            onClick={() => onPick?.(opt, i)}
          >
            <span class="k" aria-hidden="true">{i + 1}</span>
            {opt.label}
            {opt.recommended && <span class="rec">RECOMMENDED</span>}
          </button>
        ))}
      </div>
      <form class="ask-free" onSubmit={submitFree}>
        <input
          type="text"
          placeholder={placeholder}
          aria-label="Answer in your own words"
          value={free}
          onInput={(e) => setFree(e.currentTarget.value)}
        />
        <button type="submit" class="ask-free-submit" aria-label="Send answer">
          <ArrowUp size={15} />
        </button>
      </form>
    </div>
  );
}
