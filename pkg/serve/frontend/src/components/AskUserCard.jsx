import { useState } from 'preact/hooks';
import { MessageCircleQuestion, Send, SkipForward } from 'lucide-preact';
import { resolveAskUser } from '../state.js';

export function AskUserCard({ ask, sessionId }) {
  const questions = ask.questions || [];
  const [current, setCurrent] = useState(0);
  const [answers, setAnswers] = useState(() => questions.map(() => ''));
  const [customBuf, setCustomBuf] = useState('');
  const [resolved, setResolved] = useState(false);

  if (resolved || questions.length === 0) {
    return resolved ? <div class="permission-resolved">✓ Answered</div> : null;
  }

  const q = questions[current];
  const options = q.options || [];

  const selectOption = (opt) => {
    const next = [...answers];
    next[current] = opt;
    setAnswers(next);
    setCustomBuf('');
    if (current < questions.length - 1) {
      setCurrent(current + 1);
    }
  };

  const submitCustom = () => {
    const text = customBuf.trim();
    if (!text) return;
    const next = [...answers];
    next[current] = text;
    setAnswers(next);
    setCustomBuf('');
    if (current < questions.length - 1) {
      setCurrent(current + 1);
    }
  };

  const handleSubmit = async () => {
    // Ensure current answer is captured.
    const final = [...answers];
    if (!final[current] && customBuf.trim()) {
      final[current] = customBuf.trim();
    }
    // Check all answered.
    for (let i = 0; i < final.length; i++) {
      if (!final[i]) {
        setCurrent(i);
        return;
      }
    }
    setResolved(true);
    try {
      await resolveAskUser(sessionId, ask.id, final);
    } catch (e) {
      console.error('Ask user resolve failed:', e);
      setResolved(false);
    }
  };

  const handleSkip = async () => {
    const skipped = questions.map((_, i) => answers[i] || '(skipped)');
    setResolved(true);
    try {
      await resolveAskUser(sessionId, ask.id, skipped);
    } catch (e) {
      console.error('Ask user skip failed:', e);
      setResolved(false);
    }
  };

  const isLast = current === questions.length - 1;
  const allAnswered = answers.every((a) => !!a) || (answers.filter((a) => !!a).length === questions.length - 1 && customBuf.trim());

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
              if (customBuf.trim()) {
                submitCustom();
                if (isLast) handleSubmit();
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
          {!isLast && <button onClick={() => setCurrent(current + 1)}>Next →</button>}
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
