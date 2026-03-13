import { useState, useEffect } from 'preact/hooks';
import { MessageCircleQuestion, Send, SkipForward } from 'lucide-preact';
import { resolveAskUser } from '../state.js';

export function AskUserCard({ ask, sessionId }) {
  const questions = ask.questions || [];
  const [current, setCurrent] = useState(0);
  const [answers, setAnswers] = useState(() => questions.map(() => ''));
  const [customBuf, setCustomBuf] = useState('');
  const [submitting, setSubmitting] = useState(false);

  // Reset internal state when the ask ID changes (new question batch).
  useEffect(() => {
    setCurrent(0);
    setAnswers(questions.map(() => ''));
    setCustomBuf('');
    setSubmitting(false);
  }, [ask.id]);

  if (submitting || questions.length === 0) {
    return submitting
      ? <div class="permission-resolved">✓ Answered</div>
      : null;
  }

  const q = questions[current];
  const options = q.options || [];

  const selectOption = (opt) => {
    const next = [...answers];
    next[current] = opt;
    setAnswers(next);
    setCustomBuf('');
    // Auto-advance to next question if not the last.
    if (current < questions.length - 1) {
      setCurrent(current + 1);
    }
  };

  const commitCustom = () => {
    const text = customBuf.trim();
    if (!text) return false;
    const next = [...answers];
    next[current] = text;
    setAnswers(next);
    setCustomBuf('');
    return true;
  };

  const handleSubmit = async () => {
    // Capture current custom input if needed.
    const final = [...answers];
    if (!final[current] && customBuf.trim()) {
      final[current] = customBuf.trim();
    }
    // Check all questions answered.
    for (let i = 0; i < final.length; i++) {
      if (!final[i]) {
        setCurrent(i);
        return;
      }
    }
    setSubmitting(true);
    try {
      await resolveAskUser(sessionId, ask.id, final);
    } catch (e) {
      console.error('Ask user resolve failed:', e);
      setSubmitting(false);
    }
  };

  const handleSkip = async () => {
    const skipped = questions.map((_, i) => answers[i] || '(skipped)');
    setSubmitting(true);
    try {
      await resolveAskUser(sessionId, ask.id, skipped);
    } catch (e) {
      console.error('Ask user skip failed:', e);
      setSubmitting(false);
    }
  };

  const allAnswered = (() => {
    for (let i = 0; i < questions.length; i++) {
      if (!answers[i] && (i !== current || !customBuf.trim())) return false;
    }
    return true;
  })();

  return (
    <div class="ask-user-card">
      <div class="ask-user-header">
        <MessageCircleQuestion />
        {questions.length > 1
          ? <span>Question {current + 1} of {questions.length}</span>
          : <span>Question from agent</span>}
      </div>
      <div class="ask-user-question">{q.question}</div>

      {options.length > 0 && (
        <div class="ask-user-options">
          {options.map((opt, i) => (
            <button
              key={i}
              class={`ask-user-option ${answers[current] === opt ? 'selected' : ''}`}
              onClick={() => selectOption(opt)}
            >
              {opt}
            </button>
          ))}
        </div>
      )}

      <div class="ask-user-custom">
        <input
          type="text"
          placeholder="Type your own answer…"
          value={customBuf}
          onInput={(e) => setCustomBuf(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              if (customBuf.trim()) {
                commitCustom();
                if (current >= questions.length - 1) {
                  // Last question — submit all.
                  handleSubmit();
                } else {
                  // Advance to next question.
                  const next = [...answers];
                  next[current] = customBuf.trim();
                  setAnswers(next);
                  setCustomBuf('');
                  setCurrent(current + 1);
                }
              } else if (allAnswered) {
                handleSubmit();
              }
            }
          }}
          autoFocus
        />
      </div>

      {questions.length > 1 && (
        <div class="ask-user-nav">
          <button disabled={current === 0} onClick={() => setCurrent(current - 1)}>← Back</button>
          <div class="ask-user-dots">
            {questions.map((_, i) => (
              <span
                key={i}
                class={`ask-user-dot ${i === current ? 'active' : ''} ${answers[i] ? 'answered' : ''}`}
                onClick={() => setCurrent(i)}
              />
            ))}
          </div>
          {current < questions.length - 1 && (
            <button onClick={() => setCurrent(current + 1)}>Next →</button>
          )}
        </div>
      )}

      <div class="ask-user-actions">
        <button class="btn-approve" onClick={handleSubmit} disabled={!allAnswered}>
          <Send /> Submit
        </button>
        <button class="btn-deny" onClick={handleSkip}>
          <SkipForward /> Skip
        </button>
      </div>
    </div>
  );
}
