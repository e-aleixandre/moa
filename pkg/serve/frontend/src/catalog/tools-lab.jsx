import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { MobileStream } from "../layout/mobile/MobileConversationScreen/MobileStream.jsx";
import { Stream } from "../layout/Stream/Stream.jsx";
import { MobileChrome } from "../layout/mobile/MobileChrome/MobileChrome.jsx";
import { ActivityLedger } from "../components/ActivityLedger/ActivityLedger.jsx";
import { projectStream } from "../data/stream-model.js";
import { CONVERSATIONS } from "./tool-conversations.js";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "../layout/mobile/MobileConversationScreen/MobileStream.css";
import "../layout/Stream/Stream.css";
import "./tools-lab.css";

// tools-lab — CATALOG ONLY. Every kind of tool call, in every state, read the
// way the owner actually reads them: inside a transcript.
//
// The rule this lab obeys: it MOUNTS the shipped transcript. `MobileStream`
// and `Stream` are the production components (both are `ConversationStream`,
// which takes `session` + `blocks` as plain props and touches no store — the
// same reason SubagentView can mount them for a finished child). The blocks
// come from the production `projectStream`, fed raw server-shaped messages
// from tool-conversations.js. So every row on screen was built by toolPath,
// toolPreview, deriveOut, mapStatus and fuseLedgerDetails, not by this file.
// Nothing here re-implements a ledger row, and no rule below restyles one.
//
// Three conversations rather than one: the matrix is big enough that a single
// transcript stops reading like work. Each is a plausible session; between
// them they cover every tool, state, group size and ugly-content case (see
// COVERAGE in tool-conversations.js).
//
// Routes:
//   ?view=tools              the reading page (phones side by side, desktop below)
//   ?view=tools&conv=failing pick one conversation
//   ?view=toolsphone         ONE phone filling the window, real scroll

const noop = () => {};

// fuseLedgerDetails runs inside ConversationStream, so the lab hands over the
// projection and nothing else. Recomputed per render is fine: the fixtures are
// static and projectStream is pure.
function useBlocks(session) {
  return projectStream(session);
}

/* A phone-sized frame. 390x780 is the number that matters — it is where the
   ledger is read worst — so it is a real 390 CSS px, never a scaled-down
   screenshot. `.mconv` is production's own column; inside the frame it fills
   the frame instead of 100dvh, which is the single adaptation hydration-lab
   already makes for the same reason. */
function Phone({ conv, pinned = "bottom" }) {
  const blocks = useBlocks(conv.session);
  const hostRef = useRef(null);

  // The real stream sticks to the bottom, which is where a live row lives. A
  // top-anchored specimen would show the opening message and hide the state
  // the owner came to look at.
  useLayoutEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const el = host.querySelector(".zl-transcript");
    if (!el) return;
    const place = () => {
      el.scrollTop = pinned === "top" ? 0 : Math.max(0, el.scrollHeight - el.clientHeight);
    };
    place();
    const frame = requestAnimationFrame(place);
    return () => cancelAnimationFrame(frame);
  }, [conv.id, pinned]);

  return (
    <div class="mconv tools-screen" ref={hostRef}>
      <MobileStream session={conv.session} blocks={blocks} onOpenSubagent={noop} />
      <MobileChrome title={conv.session.title} onToggle={noop} onNew={noop} />
    </div>
  );
}

function PhoneSpecimen({ conv, pinned }) {
  return (
    <figure class="tools-figure" data-tools={`${conv.id}-${pinned}`}>
      <div class="tools-frame">
        <Phone conv={conv} pinned={pinned} />
      </div>
      <figcaption class="tools-cap">
        <b>{conv.label}</b>
        <span>{pinned === "top" ? "top of the transcript" : "tail — the live row"}</span>
      </figcaption>
    </figure>
  );
}

/* The desktop column, same components, wider measure. Second on purpose: the
   phone is where the ledger fails first, so it is what the page opens with. */
function Desk({ conv }) {
  const blocks = useBlocks(conv.session);
  return (
    <figure class="tools-figure is-desk" data-tools={`desk-${conv.id}`}>
      <div class="tools-desk">
        <Stream session={conv.session} blocks={blocks} onOpenSubagent={noop} />
      </div>
      <figcaption class="tools-cap">
        <b>{conv.label}</b>
        <span>desktop, 860px</span>
      </figcaption>
    </figure>
  );
}

export function ToolsLab() {
  const params = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
  const only = params.get("conv");
  const shots = params.get("shots") === "1";
  const convs = only ? CONVERSATIONS.filter((c) => c.id === only) : CONVERSATIONS;
  const list = convs.length > 0 ? convs : CONVERSATIONS;

  // ?shots=1 — the frames alone, for the capture script: no prose, nothing
  // floating over a phone that is about to be photographed.
  if (shots) {
    return (
      <div class="tools-lab is-shots">
        {list.map((c) => (
          <div class="tools-row" key={c.id}>
            <PhoneSpecimen conv={c} pinned="top" />
            <PhoneSpecimen conv={c} pinned="bottom" />
          </div>
        ))}
        {list.map((c) => <Desk conv={c} key={`d-${c.id}`} />)}
      </div>
    );
  }

  return (
    <div class="tools-lab">
      <header class="tools-intro">
        <h1>Tool calls, in a conversation</h1>
        <p>
          Three fake but plausible sessions, mounted on the SHIPPED transcript:
          production <code>MobileStream</code> / <code>Stream</code> fed by the
          production <code>projectStream</code>. The fixtures are raw
          server-shaped messages, so every row was derived by the real code
          rather than written to look right.
        </p>
        <p class="tools-note">
          Phone first — 390×780, a real 390 CSS px, because that is where these
          rows are read worst. <a href="?view=toolsphone">Open one phone with real scroll →</a>
        </p>
      </header>

      {list.map((c) => (
        <section class="tools-conv" key={c.id}>
          <h2>{c.label}</h2>
          <p class="tools-conv-note">{c.note}</p>
          <div class="tools-row">
            <PhoneSpecimen conv={c} pinned="top" />
            <PhoneSpecimen conv={c} pinned="bottom" />
          </div>
          <Desk conv={c} />
        </section>
      ))}
    </div>
  );
}

/* ?view=ledgericons — CATALOG ONLY. The twenty tool glyphs at real size, one
   per row, for the icon review. It MOUNTS `ActivityLedger` with one row per
   tool rather than drawing an icon grid: a glyph is judged where it is read,
   at 14px beside a 14px name on a 390px phone, not blown up in a specimen
   sheet. Same reason the rest of this lab mounts the shipped transcript. */
const ICON_TOOLS = [
  "read", "ls", "grep", "find", "moa_docs", "memory",
  "edit", "multiedit", "write", "apply_patch", "checkpoint", "verify", "tasks",
  "bash", "fetch_content", "web_search", "subagent", "ask_user", "load_skill",
  "mcp__linear__create_issue",
];

export function LedgerIcons() {
  const rows = ICON_TOOLS.map((tool, i) => ({
    id: `i${i}`,
    tool,
    arg: { text: "" },
    out: "",
    status: "ok",
  }));
  return (
    <div class="tools-icons" data-led-icons>
      <ActivityLedger rows={rows} folded={false} />
    </div>
  );
}

/* ?view=toolsphone — ONE phone, filling the window, with its own real scroll.

   The reading page above is right for comparing three conversations and wrong
   for actually reading one on a phone: the owner asked to be able to scroll
   it on the device, so here the frame comes off and `.mconv` is the screen. */
export function ToolsPhone() {
  const params = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
  const [id, setId] = useState(params.get("conv") || CONVERSATIONS[0].id);
  const conv = CONVERSATIONS.find((c) => c.id === id) || CONVERSATIONS[0];
  const blocks = useBlocks(conv.session);

  return (
    <div class="mconv tools-alone">
      <MobileStream session={conv.session} blocks={blocks} onOpenSubagent={noop} />
      <MobileChrome title={conv.session.title} onToggle={noop} onNew={noop} />
      <nav class="tools-pick" aria-label="Conversation">
        {CONVERSATIONS.map((c) => (
          <button
            type="button"
            key={c.id}
            class={c.id === conv.id ? "is-on" : ""}
            onClick={() => setId(c.id)}
          >
            {c.label}
          </button>
        ))}
      </nav>
    </div>
  );
}
