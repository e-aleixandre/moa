import { useRef, useEffect } from 'preact/hooks';

/**
 * FileSuggestions — pure render component for file picker dropdown.
 * All state (items, cursor) is owned by InputBar.
 *
 * @param {Object} props
 * @param {Array<{path: string, is_dir: boolean}>} props.items
 * @param {number} props.cursor - selected index
 * @param {(path: string, isDir: boolean) => void} props.onSelect - click handler
 * @param {(index: number) => void} props.onHover - mouse enter
 */
export function FileSuggestions({ items, cursor, onSelect, onHover }) {
  const listRef = useRef(null);

  // Scroll selected item into view.
  useEffect(() => {
    if (!listRef.current) return;
    const el = listRef.current.children[cursor];
    if (el) el.scrollIntoView({ block: 'nearest' });
  }, [cursor]);

  if (!items || items.length === 0) return null;

  return (
    <div class="file-suggestions" ref={listRef}>
      {items.map((item, i) => (
        <div
          key={item.path}
          class={`file-suggestion-item ${i === cursor ? 'selected' : ''} ${item.is_dir ? 'is-dir' : ''}`}
          onMouseDown={(e) => { e.preventDefault(); onSelect(item.path, item.is_dir); }}
          onMouseEnter={() => onHover(i)}
        >
          <span class="file-suggestion-icon">{item.is_dir ? '▸' : '╶'}</span>
          <span class="file-suggestion-path">{item.path}</span>
        </div>
      ))}
    </div>
  );
}
