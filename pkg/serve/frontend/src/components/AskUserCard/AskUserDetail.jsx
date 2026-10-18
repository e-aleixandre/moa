import { Check, PenLine } from "lucide-preact";
import { parseMultiAnswer, splitAskResponder } from "../../data/ask-user-machine.js";

export { splitAskResponder };
import "./AskUserDetail.css";

export function parseAskAnswers(result, count, responder) {
  if (!result) return [];
  const text = splitAskResponder(result, responder).body;
  if (count === 1) {
    const answer = text.trim();
    return answer ? [answer] : [];
  }
  return text.split("\n")
    .filter((line) => line.startsWith("A: "))
    .map((line) => line.slice(3));
}

function optionLabel(option) {
  return typeof option === "string" ? option : option?.label || "";
}

export function AskUserDetail({ questions, result, responder: recorded, posted = false }) {
  const items = Array.isArray(questions) ? questions : [];
  // A posted question's result is only the server's acknowledgement: no
  // answer exists yet, so none is read out of it.
  const answers = posted ? [] : parseAskAnswers(result, items.length, recorded);
  const { responder } = posted ? { responder: null } : splitAskResponder(result, recorded);

  return (
    <div class="ask-detail">
      {responder && <div class="ask-detail-responder">Answered by {responder}</div>}
      {items.map((item, index) => {
        const question = item?.question || "";
        const options = Array.isArray(item?.options) ? item.options : [];
        const answer = answers[index] || "";
        const skipped = answer === "(skipped)";
        const labels = options.map(optionLabel);
        const multi = item?.multiple === true && !skipped;
        const parsed = multi ? parseMultiAnswer(answer, labels) : null;
        const custom = multi
          ? parsed.free
          : answer && !skipped && !labels.includes(answer);

        return (
          <section class="ask-detail-question" key={index}>
            <div class="ask-detail-text">{question}</div>
            {options.length > 0 && (
              <div class="ask-detail-options">
                {options.map((option, optionIndex) => {
                  const label = optionLabel(option);
                  const chosen = !skipped && (multi ? parsed.picked.includes(label) : label === answer);
                  return (
                    <span class={`ask-detail-option${chosen ? " chosen" : ""}`} key={optionIndex}>
                      {chosen && <Check size={12} aria-hidden="true" />}
                      {label}
                    </span>
                  );
                })}
              </div>
            )}
            {skipped ? (
              <div class="ask-detail-answer skipped">Skipped</div>
            ) : custom ? (
              <div class="ask-detail-answer custom">
                <PenLine size={12} aria-hidden="true" />
                {multi ? parsed.free : answer}
              </div>
            ) : posted ? (
              <div class="ask-detail-answer missing">Asked</div>
            ) : options.length === 0 ? (
              <div class="ask-detail-answer">{answer || "—"}</div>
            ) : !answer ? (
              <div class="ask-detail-answer missing">—</div>
            ) : null}
          </section>
        );
      })}
    </div>
  );
}
