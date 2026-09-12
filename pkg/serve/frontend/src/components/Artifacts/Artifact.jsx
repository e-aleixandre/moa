import "./Artifact.css";

function FileGlyph() {
  return (
    <svg viewBox="0 0 20 24">
      <path d="M2.75 1h8.5L17.25 7v15.25a.75.75 0 0 1-.75.75h-13a.75.75 0 0 1-.75-.75V1.75A.75.75 0 0 1 2.75 1z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
      <path d="M11.25 1v5.25a.75.75 0 0 0 .75.75h5.25" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
    </svg>
  );
}

function ArrowGlyph() {
  return (
    <svg viewBox="0 0 16 16">
      <path d="M8 2.5v8M4.5 7L8 10.5 11.5 7M3 13h10" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function metaLine(kind, size) {
  if (kind && size) return `${kind} · ${size}`;
  return kind || size || "";
}

// Artifact — a deliverable on the page. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx `Artifact`, zones-lab.css `.zl-art*`), MOVED here
// rather than imitated. Raised one step above the page, a real file glyph,
// and the whole card is the open action. The catalogue imports this component
// now, which is what makes one definition rather than two.
//
// What is NOT the catalogue's is a second target (download / share) when the
// caller passes `onAction`. The prototype had one button; production still
// has to let you take the file without opening it.
export function Artifact({
  name,
  kind,
  size,
  dense,
  onOpen,
  onAction,
  actionLabel,
  busy,
  extra,
  className = "",
}) {
  const split = typeof onAction === "function";
  const label = `Open artifact ${name}`;
  const meta = metaLine(kind, size);
  const cls = `zl-art${dense ? " is-dense" : ""}${className ? ` ${className}` : ""}`;

  if (!split) {
    return (
      <button type="button" class={cls} onClick={onOpen} aria-label={label}>
        <span class="zl-art-ico" aria-hidden="true"><FileGlyph /></span>
        <span class="zl-art-main">
          <span class="zl-art-name">{name}</span>
          {meta && <span class="zl-art-meta zl-data">{meta}</span>}
        </span>
        <span class="zl-art-act" aria-hidden="true"><ArrowGlyph /></span>
      </button>
    );
  }

  return (
    <div class={cls}>
      <button
        type="button"
        class="zl-art-open"
        onClick={onOpen}
        disabled={!onOpen}
        aria-label={label}
      >
        <span class="zl-art-ico" aria-hidden="true"><FileGlyph /></span>
        <span class="zl-art-main">
          <span class="zl-art-name">{name}</span>
          {meta && <span class="zl-art-meta zl-data">{meta}</span>}
        </span>
      </button>
      {extra}
      <button
        type="button"
        class="zl-art-act is-btn"
        onClick={onAction}
        disabled={busy}
        aria-label={actionLabel || `Download ${name}`}
      >
        <ArrowGlyph />
      </button>
    </div>
  );
}

export function artifactKind(file) {
  const name = file?.name || "";
  const ext = name.includes(".") ? name.split(".").pop() : "";
  return ext || file?.mime || "file";
}
