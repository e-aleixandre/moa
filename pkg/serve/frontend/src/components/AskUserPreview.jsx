import { Check, PenLine } from 'lucide-preact';

/**
 * Renders ask_user tool call content as a styled Q&A block
 * instead of raw text in a <pre>.
 */
export function AskUserPreview({ args, result }) {
  const questions = args?.questions;
  if (!Array.isArray(questions) || questions.length === 0) return null;

  const answers = parseAnswers(result, questions.length);

  return (
    <div class="ask-preview">
      {questions.map((q, i) => (
        <AskQuestion key={i} question={q} answer={answers[i] || null} />
      ))}
    </div>
  );
}

function AskQuestion({ question, answer }) {
  const text = question.question || '';
  const options = question.options || [];
  const isCustom = answer && options.length > 0 && !options.includes(answer);

  return (
    <div class="ask-preview-question">
      <div class="ask-preview-text">{text}</div>
      {options.length > 0 && (
        <div class="ask-preview-options">
          {options.map((opt, i) => {
            const selected = opt === answer;
            return (
              <span key={i} class={`ask-preview-option ${selected ? 'selected' : ''}`}>
                {selected && <Check class="ask-preview-check" />}
                {opt}
              </span>
            );
          })}
          {isCustom && (
            <span class="ask-preview-option selected custom">
              <PenLine class="ask-preview-check" />
              {answer}
            </span>
          )}
        </div>
      )}
      {options.length === 0 && answer && (
        <div class="ask-preview-free-answer">{answer}</div>
      )}
    </div>
  );
}

function parseAnswers(result, count) {
  if (!result) return [];
  if (count === 1) return [result.trim()];
  const answers = [];
  for (const line of result.split('\n')) {
    if (line.startsWith('A: ')) answers.push(line.substring(3));
  }
  return answers;
}
