import { useRef, useEffect } from "preact/hooks";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";
import { StateDot } from "../../primitives/StateDot/StateDot.jsx";
import "./MentionSuggestions.css";

// MentionSuggestions — the @ menu above the composer: Owners, Sessions and
// Files in one list, each group under its own header. All state (items, cursor)
// belongs to the Composer; `items` is the flat, already-grouped list from
// flattenMentionGroups, so ↑/↓ walk it straight through the headers.
//
// @param {Array} items - rows with { kind: 'owner'|'session'|'file', group, first, ... }
// @param {number} cursor - selected index
// @param {(item) => void} onSelect - mousedown handler
// @param {(index: number) => void} onHover - mouse enter

function relAge(updated) {
  if (!updated) return "";
  const min = Math.floor((Date.now() - Date.parse(updated)) / 60000);
  if (!(min >= 0)) return "";
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  return h < 24 ? `${h}h` : `${Math.floor(h / 24)}d`;
}

function Row({ item }) {
  if (item.kind === "owner") {
    return (
      <>
        <span class="mentionsug-lead"><OwnerAvatarFor owner={item.owner} state="idle" size={24} /></span>
        <span class="mentionsug-name">{item.label}</span>
        {item.project && <span class="mentionsug-sub">{item.project}</span>}
        {item.saved && <span class="mentionsug-chip">saved</span>}
      </>
    );
  }
  if (item.kind === "session") {
    const age = relAge(item.updated);
    return (
      <>
        <span class="mentionsug-lead"><StateDot state={item.saved ? "saved" : (item.state || "idle")} size={8} /></span>
        <span class="mentionsug-name">{item.label}</span>
        <span class="mentionsug-meta">
          {item.saved && <span class="mentionsug-chip">saved</span>}
          {age}
        </span>
      </>
    );
  }
  return (
    <>
      <span class="mentionsug-lead mentionsug-icon">{item.is_dir ? "▸" : "╶"}</span>
      <span class="mentionsug-path">{item.path}</span>
    </>
  );
}

export function MentionSuggestions({ items, cursor, onSelect, onHover }) {
  const listRef = useRef(null);

  // Scroll the selected row into view.
  useEffect(() => {
    if (!listRef.current) return;
    const el = listRef.current.querySelector(`[data-index="${cursor}"]`);
    if (el) el.scrollIntoView({ block: "nearest" });
  }, [cursor]);

  if (!items || items.length === 0) return null;

  return (
    <div class="mentionsug-list" ref={listRef} role="listbox" aria-label="Mention suggestions">
      {items.map((item, i) => [
        item.first && <div class="mentionsug-group" key={`g-${item.group}`} role="presentation">{item.group}</div>,
        <div
          key={`${item.kind}:${item.id || item.path}`}
          data-index={i}
          role="option"
          aria-selected={i === cursor}
          class={`mentionsug-item ${i === cursor ? "selected" : ""} ${item.is_dir ? "is-dir" : ""} is-${item.kind}`}
          onMouseDown={(e) => { e.preventDefault(); onSelect(item); }}
          onMouseEnter={() => onHover(i)}
        >
          <Row item={item} />
        </div>,
      ])}
    </div>
  );
}
