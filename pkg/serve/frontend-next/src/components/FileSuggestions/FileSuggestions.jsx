import { useRef, useEffect } from "preact/hooks";
import "./FileSuggestions.css";

// FileSuggestions — pure render component for the @-mention file picker
// dropdown. All state (items, cursor) is owned by the Composer. Ported 1:1
// from pkg/serve/frontend/src/components/FileSuggestions.jsx, renamed classes
// with a `filesug-` prefix to avoid colliding with the old SPA's CSS while
// both frontends are served side by side.
//
// @param {Object} props
// @param {Array<{path: string, is_dir: boolean}>} props.items
// @param {number} props.cursor - selected index
// @param {(path: string, isDir: boolean) => void} props.onSelect - click handler
// @param {(index: number) => void} props.onHover - mouse enter
export function FileSuggestions({ items, cursor, onSelect, onHover }) {
  const listRef = useRef(null);

  // Scroll selected item into view.
  useEffect(() => {
    if (!listRef.current) return;
    const el = listRef.current.children[cursor];
    if (el) el.scrollIntoView({ block: "nearest" });
  }, [cursor]);

  if (!items || items.length === 0) return null;

  return (
    <div class="filesug-list" ref={listRef}>
      {items.map((item, i) => (
        <div
          key={item.path}
          class={`filesug-item ${i === cursor ? "selected" : ""} ${item.is_dir ? "is-dir" : ""}`}
          onMouseDown={(e) => { e.preventDefault(); onSelect(item.path, item.is_dir); }}
          onMouseEnter={() => onHover(i)}
        >
          <span class="filesug-icon">{item.is_dir ? "▸" : "╶"}</span>
          <span class="filesug-path">{item.path}</span>
        </div>
      ))}
    </div>
  );
}
