import { useState } from "preact/hooks";

// PreviewReference — "anclado al lomo": the reference to an element you pointed
// at in the Live Preview hangs off the message's own peach spine with a short
// dash, like a tag tied to it. It says what you meant (the element's visible
// text), one disambiguator (the containing card) and the page — nothing else.
// The full selector still exists, one tap away, so it never competes with the
// comment you actually wrote.
export function PreviewReference({ reference }) {
  const [open, setOpen] = useState(false);
  if (!reference) return null;

  const { quote, tagMark, context, path, target, selector, ancestors, attrs } = reference;
  const hasDetail = Boolean(target || selector || ancestors?.length || attrs);
  const spoken = [tagMark, quote].filter(Boolean).join(" ");

  return (
    <>
      <button
        type="button"
        class={`ref-spine${open ? " is-on" : ""}`}
        aria-expanded={hasDetail ? open : undefined}
        onClick={() => hasDetail && setOpen((v) => !v)}
        title={target || undefined}
      >
        {tagMark && <span class="ref-tag">{tagMark}</span>}
        {quote && <span class="ref-spine-q">{quote}</span>}
        {context && <span class="ref-spine-ctx">{context}</span>}
        {path && <span class="ref-spine-page">{path}</span>}
      </button>
      {open && hasDetail && (
        <div class="ref-detail" aria-label={`Selector for ${spoken || "the selected element"}`}>
          {target && <code>{target}</code>}
          {selector && selector !== target && <span>{selector}</span>}
          {ancestors?.length > 0 && <span>{ancestors.join(" › ")}</span>}
          {attrs && <span>{attrs}</span>}
        </div>
      )}
    </>
  );
}
