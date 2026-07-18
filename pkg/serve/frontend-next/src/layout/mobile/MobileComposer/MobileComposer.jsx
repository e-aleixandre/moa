import { ArrowUp } from "lucide-preact";
import "./MobileComposer.css";

// MobileComposer — bottom input for the mobile conversation. Real editable
// textarea (font-size:var(--text-input) mandatory to avoid iOS
// auto-zoom) + send button. Below, the mono status line with current
// work, tokens and today's spend. Respects the bottom safe-area-inset.
export function MobileComposer({ status, up, down, spend }) {
  return (
    <div class="mcomposer">
      <div class="mcomposer-box">
        <textarea
          class="mcomposer-input"
          rows={1}
          placeholder="Message moa…"
          aria-label="Message moa"
        />
        <button type="button" class="mcomposer-send" aria-label="Send">
          <ArrowUp size={16} aria-hidden="true" />
        </button>
      </div>
      <div class="mcomposer-status">
        <span class="work">● {status}</span>
        <span class="tokens">
          ↑{up} ↓{down}
        </span>
        <span class="spend">{spend} today</span>
      </div>
    </div>
  );
}
