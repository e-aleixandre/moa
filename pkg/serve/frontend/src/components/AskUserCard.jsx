import { useState, useEffect } from 'preact/hooks';
import { MessageCircleQuestion, Send, SkipForward } from 'lucide-preact';
import { resolveAskUser } from '../session-actions.js';

export function AskUserCard({ ask, sessionId }) {
  const questions = ask.questions || [];
  const [current, setCurrent] = useState(0);
  const [answers, setAnswers] = useState(() => questions.map(() => ''));
  const [submitting, setSubmitting] = useState(false);

  // Reset internal state when the ask ID changes (new question batch).
  useEffect(() => {
    setCurrent(0);
    setAnswers(questions.map(() => ''));
    setSubmitting(false);
  }, [ask.id]);

  if (submitting || questions.length === 0) {
    return submitting
      ? <div class="permission-resolved">✓ Answered</div>
      : null;
  }

  const q = questions[current];
  const options = q.options || [];

  // Each answer belongs to its own question: the custom input is bound directly
  // to answers[current], so navigating between questions never bleeds one
  // answer into another. The input shows the current answer only when it's a
  // free-text answer (not one of the options — an option is shown via the
  // highlighted button instead).
  const customValue = options.includes(answers[current]) ? '' : answers[current];

  const setAnswer = (val) => {
    setAnswers(prev => {
      const next = [...prev];
      next[current] = val;
      return next;
    });
  };

  const selectOption = (opt) => {
    setAnswer(opt);
    // Auto-advance to next question if not the last.
    if (current < questions.length - 1) {
      setCurrent(current + 1);
    }
  };

  const handleSubmit = async () => {
    const final = answers.map(a => a.trim());
    // Jump to the first unanswered question instead of submitting.
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
    const skipped = questions.map((_, i) => answers[i].trim() || '(skipped)');
    setSubmitting(true);
    try {
      await resolveAskUser(sessionId, ask.id, skipped);
    } catch (e) {
      console.error('Ask user skip failed:', e);
      setSubmitting(false);
    }
  };

  const allAnswered = answers.every(a => a.trim());

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
          key={current}
          type="text"
          placeholder="Type your own answer…"
          value={customValue}
          onInput={(e) => setAnswer(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              if (current < questions.length - 1) {
                setCurrent(current + 1);
              } else {
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
