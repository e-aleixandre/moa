import { mentionRanges } from "../../data/util/mentions.js";

// MentionMirror — the layer behind the composer's textarea that lights up the
// live @owner / @session mentions. It repeats the textarea's text in the same
// typography and padding (it borrows .zl-ta), invisible except for the <mark>s,
// so a mention reads as highlighted while the textarea stays plain text the
// user edits normally. aria-hidden: it is decoration, the textarea is the field.
export function MentionMirror({ text, mentions, mirrorRef }) {
  const parts = [];
  let at = 0;
  for (const { start, end, mention } of mentionRanges(text, mentions)) {
    if (start > at) parts.push(text.slice(at, start));
    parts.push(<mark class={`zl-mention-mark is-${mention.type}`}>{text.slice(start, end)}</mark>);
    at = end;
  }
  parts.push(text.slice(at));
  // The zero-width space keeps a trailing newline from collapsing, so the
  // mirror is as tall as the textarea.
  parts.push("\u200b");
  return (
    <div class="zl-ta zl-ta-mirror" aria-hidden="true" ref={mirrorRef}>
      {parts}
    </div>
  );
}
