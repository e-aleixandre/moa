import { useState, useRef, useCallback } from "preact/hooks";
import { MessageCircleQuestionMark, ArrowUp, Check, Mic, Loader2 } from "lucide-preact";
import { Field } from "../../primitives/index.js";
import "./AskUserCard.css";

const isTextEntryTarget = (el) => {
  if (!el) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || el.isContentEditable;
};

const LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";

// AskUserCard — the agent's question, in the shape the owner picked (the
// aicss approval card, 14-sep): a glyph-and-title head, the question, then
// the options as tall bordered rows with a letter in a well on the left,
// and the free answer as the LAST row rather than a field under the list.
// Letters are real shortcuts: the card is focusable (a click anywhere in it
// gives it focus) and A..Z with focus in it, outside the text field, picks
// that option. `options`: [{ label, recommended? }].
//
// The free-text field is uncontrolled by default (keeps its own state — used
// by the gallery), but becomes controlled when `onFreeChange` is passed:
// then every keystroke is reported up so a stateful container (AskUserPrompt)
// can persist the answer per question and restore it when navigating back.
//
// `voice` is the optional tap-to-talk wiring from useVoiceGesture (handlers +
// recording/transcribing). Passing it puts the mic inside the free row — the
// same tap-to-record control the composer uses; omitting it leaves a plain
// send, which is what the gallery renders.
export function AskUserCard({
  question,
  options = [],
  currentAnswer = "",
  onPick,
  onSubmitFree,
  freeValue,
  onFreeChange,
  voice = null,
  placeholder = "Something else…",
  ...rest
}) {
  const controlled = onFreeChange != null;
  const [freeInternal, setFreeInternal] = useState("");
  const free = controlled ? (freeValue || "") : freeInternal;
  const setFree = controlled ? onFreeChange : setFreeInternal;
  const rootRef = useRef(null);
  const freeLetter = LETTERS[options.length] || "";
  // The free row is "chosen" when the answer is text that is not an option.
  const freeChosen = !!free.trim() && !options.some((o) => o.label === currentAnswer);

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
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      const key = event.key;
      if (typeof key !== "string" || key.length !== 1) return;
      const idx = LETTERS.indexOf(key.toUpperCase());
      if (idx === -1) return;
      if (idx < options.length) {
        event.preventDefault();
        onPick?.(options[idx], idx);
      } else if (idx === options.length) {
        // The free row's own letter: jump into its field.
        event.preventDefault();
        rootRef.current?.querySelector(".ask-free-field .field-input")?.focus();
      }
    },
    [options, onPick]
  );

  return (
    <div class="ask" ref={rootRef} tabIndex={-1} onKeyDown={onKeyDown} {...rest}>
      <div class="ask-head">
        <span class="ask-glyph" aria-hidden="true">
          <MessageCircleQuestionMark size={15} />
        </span>
        <span class="who">moa asks</span>
      </div>
      <p class="ask-q">{question}</p>
      <div class="ask-opts">
        {options.map((opt, i) => {
          const chosen = opt.label === currentAnswer;
          return (
            <button
              key={opt.label ?? i}
              type="button"
              class={`ask-opt${chosen ? " chosen" : ""}`}
              aria-pressed={chosen}
              aria-keyshortcuts={LETTERS[i]}
              onClick={() => onPick?.(opt, i)}
            >
              <span class="k" aria-hidden="true">{LETTERS[i]}</span>
              <span class="ask-opt-label">{opt.label}</span>
              {opt.recommended && <span class="rec">RECOMMENDED</span>}
              <span class="ask-opt-check" aria-hidden="true">
                {chosen && <Check size={15} />}
              </span>
            </button>
          );
        })}
        <form class={`ask-opt ask-free${freeChosen ? " chosen" : ""}`} onSubmit={submitFree}>
          <span class="k" aria-hidden="true">{freeLetter}</span>
          <Field
            variant="box"
            size="md"
            class="ask-free-field"
            type="text"
            placeholder={voice?.supported ? `${placeholder} or tap the mic to talk` : placeholder}
            aria-label="Answer in your own words"
            aria-keyshortcuts={freeLetter}
            autocomplete="off"
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
    </div>
  );
}

// renderVoiceSubmit — the submit button doubling as the mic, mirroring the
// composer's send button: with an empty field it is the mic (tap to record,
// tap again to stop), and the moment there is an answer to send it is the
// submit arrow again. Keeping one control (instead of adding a second mic
// button) means the card gains dictation without growing a new thing to aim at
// on a phone, and because the two faces never overlap a tap is never ambiguous.
function renderVoiceSubmit(voice, free) {
  const { recording, transcribing, handlers } = voice;
  const micMode = !free.trim();
  // While the mic is live or transcribing it keeps the button, even if a
  // partial transcript has already made the field non-empty: showing Send
  // there would hide a running recording and leave no way to stop it.
  const voiceOwnsButton = micMode || recording || transcribing;

  let icon = <ArrowUp size={15} />;
  if (transcribing) icon = <Loader2 size={15} class="spin" />;
  else if (voiceOwnsButton) icon = <Mic size={15} />;

  const title = transcribing ? "Transcribing…"
    : recording ? "Tap to stop & transcribe"
    : voiceOwnsButton ? "Tap to talk"
    : "Send answer";

  const cls = [
    "ask-free-submit",
    voiceOwnsButton ? "gesture" : "",
    recording ? "recording" : "",
    transcribing ? "transcribing" : "",
    voiceOwnsButton && !recording && !transcribing ? "mic-mode" : "",
  ].filter(Boolean).join(" ");

  return (
    <div class="ask-free-send-wrap">
      <button
        type={voiceOwnsButton ? "button" : "submit"}
        class={cls}
        aria-label={voiceOwnsButton ? (recording ? "Stop recording" : "Record answer") : "Send answer"}
        title={title}
        disabled={transcribing}
        {...(voiceOwnsButton ? handlers : {})}
      >
        {icon}
      </button>
    </div>
  );
}
