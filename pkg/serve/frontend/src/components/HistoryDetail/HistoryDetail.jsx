import { parseHistoryResult } from "../../data/history-result.js";
import "./HistoryDetail.css";

// HistoryDetail shows what a `history` call retrieved: the messages it found
// or the window it opened, each with who wrote it, when and its text. The
// requested message of a window is marked.
export function HistoryDetail({ action, result }) {
  const parsed = parseHistoryResult(action, result);
  if (!parsed) return <pre class="history-detail-raw">{String(result || "")}</pre>;
  return (
    <div class={`history-detail ${parsed.action}`}>
      {parsed.summary && <div class="history-detail-summary">{parsed.summary}</div>}
      {parsed.items.map((item) => (
        <article class={`history-hit${item.focus ? " focus" : ""}`} key={`${item.pos}-${item.id}`}>
          <div class="history-hit-meta">
            <span class="history-hit-author">{item.author}</span>
            {item.date && <span>{item.date}</span>}
            <span class="history-hit-pos" title={item.id}>#{item.pos}</span>
            {item.focus && <span class="history-hit-requested">requested</span>}
            {item.fromByte > 0 && <span>from byte {item.fromByte}</span>}
          </div>
          {item.text && <div class="history-hit-text">{item.text}</div>}
        </article>
      ))}
      {parsed.notes.map((note, i) => <div class="history-detail-note" key={i}>{note}</div>)}
    </div>
  );
}
