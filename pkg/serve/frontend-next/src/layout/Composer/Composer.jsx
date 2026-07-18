import { useState } from "preact/hooks";
import { Plus, Slash, ArrowUp } from "lucide-preact";
import { Chip } from "../../primitives/index.js";
import "./Composer.css";

// Composer — área de entrada del mensaje: textarea + barra inferior con
// attach, slash commands, nota de cola y botón de enviar.
export function Composer({
  value,
  onChange,
  onSend,
  onAttach,
  onSlash,
  queued = { count: 1, note: "then update the changelog" },
  placeholder = "Message moa — Enter to send, ⇧Enter for a new line, ⌥Enter to queue…",
}) {
  const [inner, setInner] = useState("");
  const text = value ?? inner;

  function handleInput(e) {
    const v = e.currentTarget.value;
    if (onChange) onChange(v);
    else setInner(v);
  }

  return (
    <div class="composer-wrap">
      <div class="composer">
        <textarea
          rows={1}
          class="composer-textarea"
          aria-label="Message moa"
          placeholder={placeholder}
          value={text}
          onInput={handleInput}
        />
        <div class="composer-bar">
          <button type="button" class="composer-btn" title="Attach" aria-label="Attach" onClick={onAttach}>
            <Plus size={15} />
          </button>
          <button type="button" class="composer-btn" title="Slash commands" aria-label="Slash commands" onClick={onSlash}>
            <Slash size={14} />
          </button>
          {queued && (
            <div class="queue-note">
              <Chip size="sm" mono>{queued.count} queued</Chip>
              <span>“{queued.note}”</span>
            </div>
          )}
          <button type="button" class="composer-send" aria-label="Send" onClick={onSend}>
            <ArrowUp size={16} />
          </button>
        </div>
      </div>
    </div>
  );
}
