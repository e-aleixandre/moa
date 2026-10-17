import { useRef, useLayoutEffect } from "preact/hooks";
import { render } from "preact";
import { AtSign } from "lucide-preact";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";
import { StateDot } from "../../primitives/StateDot/StateDot.jsx";
import { renderUserMarkdown } from "../../data/util/markdown.js";
import { useStore } from "../../hooks/useStore.js";
import { openSession } from "../../data/tile-actions.js";
import "./MentionedText.css";

// MentionedText — a user message whose text carries @owner / @session mentions.
// The markdown pass leaves a `.md-mention-N` span where each @label is (the
// message's own label, not the target's title today); this paints the pill into
// it: the owner's face or the session's dot, the label, and — when the target is
// gone from the stores — a grey, untouchable "deleted". A pill opens its
// conversation.

// mentionTarget resolves one parsed mention against the stores.
export function mentionTarget(mention, owners, sessionIds) {
  if (mention.type === "owner") {
    const owner = (owners || []).find((o) => o.id === mention.id);
    return owner ? { owner, open: owner.session_id || "" } : null;
  }
  return sessionIds.has(mention.id) ? { open: mention.id } : null;
}

function Pill({ mention, target }) {
  return (
    <>
      <span class="md-mention-lead">
        {mention.type === "owner"
          ? (target ? <OwnerAvatarFor owner={target.owner} state="idle" size={16} /> : <AtSign size={12} aria-hidden="true" />)
          : <StateDot state="saved" size={7} />}
      </span>
      {`@${mention.label}`}
      {!target && <span class="md-mention-tag">deleted</span>}
    </>
  );
}

export function MentionedText({ text, mentions }) {
  const ref = useRef(null);
  const owners = useStore((s) => s.owners?.list);
  // A primitive, so the message re-renders only when a target appears or goes.
  const present = useStore((s) => mentions
    .filter((m) => m.type === "session" && s.sessions[m.id])
    .map((m) => m.id)
    .join(","));
  const html = renderUserMarkdown(text, mentions);

  useLayoutEffect(() => {
    const root = ref.current;
    if (!root) return undefined;
    const sessionIds = new Set(present ? present.split(",") : []);
    const spans = [];
    for (const span of root.querySelectorAll(".md-mention")) {
      const index = Number((/md-mention-(\d+)/.exec(span.className) || [])[1]);
      const mention = mentions[index];
      if (!mention) continue;
      const target = mentionTarget(mention, owners, sessionIds);
      if (!span.dataset.painted) span.textContent = "";
      span.dataset.painted = "1";
      span.className = `md-mention md-mention-${index} is-${mention.type}${target ? "" : " is-deleted"}`;
      if (target?.open) {
        span.setAttribute("role", "button");
        span.setAttribute("tabindex", "0");
        span.dataset.open = target.open;
      } else {
        span.removeAttribute("role");
        span.removeAttribute("tabindex");
        delete span.dataset.open;
      }
      render(<Pill mention={mention} target={target} />, span);
      spans.push(span);
    }
    return () => { for (const span of spans) render(null, span); };
  }, [html, mentions, owners, present]);

  const open = (e) => {
    const id = e.target.closest?.(".md-mention")?.dataset.open;
    if (!id) return;
    e.preventDefault();
    openSession(id);
  };

  return (
    <div
      class="zl-prose wp-md"
      ref={ref}
      onClick={open}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") open(e); }}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}
