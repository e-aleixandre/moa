import { useState, useEffect, useRef, useCallback } from "preact/hooks";
import { AskUserCard } from "./AskUserCard.jsx";
import { resolveAskUser } from "../../data/session-actions.js";
import { usePendingPromptAttentionReceipt } from "../AttentionReceipt/AttentionReceipt.jsx";
import { useVoiceGesture } from "../../hooks/useVoiceGesture.js";
import { useCanTranscribe } from "../../hooks/useCanTranscribe.js";
import {
  initAnswers, setAnswer, firstUnanswered, allAnswered, skipAnswers, appendDictation,
} from "../../data/ask-user-machine.js";
import "./AskUserPrompt.css";

// AskUserPrompt — stateful container that drives the multi-question ask_user
// flow around the single-question AskUserCard mock. State transitions are the
// pure ask-user-machine.js (see src/data/ask-user-machine.js and its tests);
// this component only wires that machine to the visual card + resolveAskUser,
// porting the old SPA's AskUserCard.jsx (pkg/serve/frontend/src/components/
// AskUserCard.jsx) semantics: per-question answers that never bleed into each
// other, back/next + clickable dots, Submit jumps to the first unanswered
// question, Skip fills the blanks with '(skipped)', and picking an option
// auto-advances to the next question.
export function AskUserPrompt({ session }) {
  const ask = session.pendingAsk;
  const questions = ask?.questions || [];

  const [current, setCurrent] = useState(0);
  const [answers, setAnswers] = useState(() => initAnswers(questions));
  const [submitting, setSubmitting] = useState(false);
  const [voiceError, setVoiceError] = useState(null);
  // Synchronous in-flight guard: `submitting` is reactive state, so two
  // activations before the next render could both fire resolveAskUser. This ref
  // latches the moment a resolve starts and is only released on error / on a
  // new ask batch, guaranteeing a single resolution.
  const resolvingRef = useRef(false);

  // Reset whenever the ask batch changes (new ask.id) — mirrors the old
  // card's `useEffect(..., [ask.id])`.
  useEffect(() => {
    setCurrent(0);
    setAnswers(initAnswers(questions));
    setSubmitting(false);
    setVoiceError(null);
    resolvingRef.current = false;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ask?.id]);

  // `current` and the submit action are read through refs by the voice hook:
  // a transcript (or a tap-to-send) resolves asynchronously, and the hook keeps
  // its first callback identity, so without these it would target whatever
  // question was on screen at mount.
  const currentRef = useRef(current);
  currentRef.current = current;
  const questionsRef = useRef(questions);
  questionsRef.current = questions;
  const submitFreeRef = useRef(null);
  // Scopes the keyboard shortcut to this card (see the effect below).
  const rootRef = useRef(null);
  usePendingPromptAttentionReceipt(rootRef, session.id, ask);

  // The ask batch a recording belongs to. A transcription resolves
  // asynchronously, so one started under a previous batch (or after the answer
  // was submitted) must be dropped rather than typed into a different question.
  const askIdRef = useRef(ask?.id);
  askIdRef.current = ask?.id;
  const recordingAskRef = useRef(null);

  // Dictation appends to what is already there, so an answer can be spoken in
  // several passes; if the current answer is a picked option, speaking replaces
  // it (see appendDictation).
  const insertDictation = useCallback((text) => {
    if (recordingAskRef.current !== askIdRef.current) return;
    const idx = currentRef.current;
    const opts = questionsRef.current[idx]?.options || [];
    setAnswers((prev) => appendDictation(prev, idx, text, opts));
    // A successful transcription clears a previous failure: leaving "No
    // microphone found" under a working answer is just confusing.
    setVoiceError(null);
  }, []);

  const canTranscribe = useCanTranscribe();
  const voice = useVoiceGesture({
    onTranscript: insertDictation,
    onError: setVoiceError,
    onSend: () => submitFreeRef.current?.(),
  });

  // Stamp each recording with the batch that started it, and clear a stale
  // error the moment a new attempt begins.
  useEffect(() => {
    if (voice.recording) {
      recordingAskRef.current = askIdRef.current;
      setVoiceError(null);
    }
  }, [voice.recording]);

  // ⌘. / Alt+. mirrors the composer's shortcut. The event is claimed with
  // preventDefault + stopPropagation so no other listener on window (the
  // composer's, another pane's question) also starts recording. Ownership: if
  // focus sits inside some question card, only that card reacts — otherwise
  // every visible question in a grid would record at once. With focus nowhere
  // in particular (body, right after the prompt appeared) the shortcut must
  // still work, so a card with no competing focus takes it.
  const voiceUsable = canTranscribe && voice.supported;
  const toggleVoice = voice.toggleFromShortcut;
  useEffect(() => {
    if (!voiceUsable || !ask?.id || submitting) return undefined;
    const onKey = (e) => {
      if (!((e.metaKey || e.altKey) && !e.ctrlKey && e.key === ".")) return;
      if (e.defaultPrevented) return;
      const root = rootRef.current;
      if (!root) return;
      const active = document.activeElement;
      if (active && active !== document.body) {
        const focusedCard = active.closest?.(".ask-user-prompt");
        // Focus inside another question, or in the composer/any other field:
        // that element owns the shortcut, not this card.
        if (focusedCard !== root) return;
      } else if (document.querySelectorAll(".ask-user-prompt").length > 1) {
        // Nothing focused and several questions on screen: there is no way to
        // tell which one the user meant, and claiming it would record in all
        // of them at once. Let them focus the one they want.
        return;
      }
      e.preventDefault();
      e.stopPropagation();
      toggleVoice();
    };
    // Capture phase: claim the key before the composer's bubble-phase listener.
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [voiceUsable, ask?.id, submitting, toggleVoice]);

  if (!ask || questions.length === 0) return null;
  if (submitting) return <div class="ask-user-resolved">✓ Answered</div>;

  const q = questions[current];
  const options = (q.options || []).map((label) => ({ label }));
  const currentAnswer = answers[current] || "";
  // The free-text field only echoes the current answer when it isn't one of
  // the option labels (an option is shown via the highlighted button instead).
  const freeValue = (q.options || []).includes(currentAnswer) ? "" : currentAnswer;

  const goTo = (idx) => setCurrent(Math.max(0, Math.min(questions.length - 1, idx)));

  const pick = (opt) => {
    setAnswers((prev) => setAnswer(prev, current, opt.label));
    if (current < questions.length - 1) goTo(current + 1);
  };

  // Free text is controlled: every keystroke persists into answers[current] so
  // it survives navigating between questions (and reaches Skip/Submit).
  const changeFree = (text) => {
    setAnswers((prev) => setAnswer(prev, current, text));
  };

  const submitFree = () => {
    if (current < questions.length - 1) goTo(current + 1);
    else handleSubmit();
  };
  // A tap on the record/send button routes here through the gesture reducer.
  // With nothing typed the button reads as a mic, so an accidental short tap
  // must do nothing rather than skip the question.
  submitFreeRef.current = () => {
    if (!(answers[current] || "").trim()) return;
    submitFree();
  };

  const resolve = async (finalAnswers) => {
    if (resolvingRef.current) return;
    resolvingRef.current = true;
    setSubmitting(true);
    try {
      await resolveAskUser(session.id, ask.id, finalAnswers);
    } catch (e) {
      console.error("Ask user resolve failed:", e);
      resolvingRef.current = false;
      setSubmitting(false);
    }
  };

  const handleSubmit = () => {
    const trimmed = answers.map((a) => a.trim());
    const idx = firstUnanswered(trimmed);
    if (idx !== -1) {
      goTo(idx);
      return;
    }
    resolve(trimmed);
  };

  const handleSkip = () => resolve(skipAnswers(questions, answers));

  const canSubmit = allAnswered(answers);

  return (
    <div class="ask-user-prompt" ref={rootRef}>
      {questions.length > 1 && (
        <div class="ask-user-prompt-head">
          Question {current + 1} of {questions.length}
        </div>
      )}
      <AskUserCard
        question={q.question}
        options={options}
        onPick={pick}
        onSubmitFree={submitFree}
        freeValue={freeValue}
        onFreeChange={changeFree}
        voice={voiceUsable ? voice : null}
      />
      {voiceError && (
        <div class="ask-user-prompt-voice-error" role="alert">{voiceError}</div>
      )}
      {questions.length > 1 && (
        <div class="ask-user-prompt-nav">
          <button type="button" disabled={current === 0} onClick={() => goTo(current - 1)}>
            ← Back
          </button>
          <div class="ask-user-prompt-dots">
            {questions.map((_, i) => (
              <button
                type="button"
                key={i}
                class={`ask-user-prompt-dot${i === current ? " active" : ""}${answers[i] ? " answered" : ""}`}
                aria-label={`Question ${i + 1}${answers[i] ? " (answered)" : ""}`}
                aria-current={i === current ? "true" : undefined}
                onClick={() => goTo(i)}
              />
            ))}
          </div>
          {current < questions.length - 1 ? (
            <button type="button" onClick={() => goTo(current + 1)}>Next →</button>
          ) : (
            <span class="ask-user-prompt-nav-spacer" />
          )}
        </div>
      )}
      <div class="ask-user-prompt-actions">
        <button type="button" class="ask-user-prompt-submit" onClick={handleSubmit}>
          {canSubmit ? "Submit" : "Submit — jump to unanswered"}
        </button>
        <button type="button" class="ask-user-prompt-skip" onClick={handleSkip}>
          Skip
        </button>
      </div>
    </div>
  );
}
