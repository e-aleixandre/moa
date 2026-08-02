import { useState, useRef, useCallback } from "preact/hooks";
import { Users, ArrowUp, Mic, Square, Loader2, ChevronUp } from "lucide-preact";
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
//
// `voice` is the optional push-to-talk wiring from useVoiceGesture (handlers +
// recording/transcribing/locked/showSlideHint). Passing it turns the submit
// button into the same hold-to-talk control the composer uses; omitting it
// leaves a plain submit, which is what the gallery renders.
export function AskUserCard({
  question,
  options = [],
  onPick,
  onSubmitFree,
  freeValue,
  onFreeChange,
  voice = null,
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
          placeholder={voice?.supported ? "Answer in your own words, or hold to talk…" : placeholder}
          aria-label="Answer in your own words"
          value={free}
          onInput={(e) => setFree(e.currentTarget.value)}
        />
        {voice?.supported ? renderVoiceSubmit(voice, free) : (
          <button type="submit" class="ask-free-submit" aria-label="Send answer">
            <ArrowUp size={15} />
          </button>
        )}
      </form>
    </div>
  );
}

// renderVoiceSubmit — the submit button doubling as push-to-talk, mirroring the
// composer's send button: tap submits, hold records, sliding up locks it
// hands-free. Keeping one control (instead of adding a second mic button) means
// the card gains dictation without growing a new thing to aim at on a phone.
function renderVoiceSubmit(voice, free) {
  const { recording, transcribing, locked, showSlideHint, handlers } = voice;
  const micMode = !free.trim();

  let icon = <ArrowUp size={15} />;
  if (transcribing) icon = <Loader2 size={15} class="spin" />;
  else if (recording && locked) icon = <Square size={13} />;
  else if (recording || micMode) icon = <Mic size={15} />;

  const title = transcribing ? "Transcribing…"
    : recording ? (locked ? "Tap to stop & transcribe" : "Release to transcribe · slide up to lock")
    : micMode ? "Hold to talk · tap to send"
    : "Send answer · hold to talk";

  const cls = [
    "ask-free-submit",
    "gesture",
    recording ? "recording" : "",
    locked ? "locked" : "",
    transcribing ? "transcribing" : "",
    micMode ? "mic-mode" : "",
  ].filter(Boolean).join(" ");

  return (
    <div class="ask-free-send-wrap">
      {showSlideHint && (
        <div class="ask-voice-lock-hint">
          <ChevronUp size={13} />
          <span>Slide up to lock</span>
        </div>
      )}
      <button
        type="button"
        class={cls}
        aria-label={micMode ? "Record answer" : "Send answer"}
        title={title}
        disabled={transcribing}
        {...handlers}
      >
        {icon}
      </button>
    </div>
  );
}
