import { useState, useEffect, useRef } from "preact/hooks";
import { AskUserCard } from "./AskUserCard.jsx";
import { resolveAskUser } from "../../data/session-actions.js";
import {
  initAnswers, setAnswer, firstUnanswered, allAnswered, skipAnswers,
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
    resolvingRef.current = false;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ask?.id]);

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
    <div class="ask-user-prompt">
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
      />
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
