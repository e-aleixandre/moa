import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { MobileStream } from "../layout/mobile/MobileConversationScreen/MobileStream.jsx";
import { MobileChrome } from "../layout/mobile/MobileChrome/MobileChrome.jsx";
import { Composer } from "../layout/Composer/Composer.jsx";
import { StatusStrip } from "../layout/StatusStrip/StatusStrip.jsx";
import { projectStream } from "../data/stream-model.js";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "../layout/mobile/MobileConversationScreen/MobileStream.css";
import "../layout/Stream/Stream.css";
import "./ambient-lab.css";

// ambient-lab — CATALOG ONLY. Three answers to "depth + light" on the phone's
// conversation screen, over the SAME transcript, switchable in place.
//
// The screen is the shipped one: production `MobileStream` (ConversationStream
// fed by projectStream), production `MobileChrome`, the production `zl-dock`
// with the shipped `Composer` and `StatusStrip`. Nothing below re-implements a
// row or a capsule. What each treatment changes is only what sits BEHIND and
// BETWEEN them: the light layer, the veil `.mconv` paints over it, and how the
// surfaces separate from the canvas. All of that lives in ambient-lab.css under
// `.amb[data-t]`; shell.css and the two conversation screens are untouched.
//
// `current` is the reference: the stage goes transparent so production's own
// aurora (body::before/::after in shell.css) and the 0.78 veil show through
// exactly as they ship.
//
// Routes (read once at load; the picker then switches in place, without a
// reload and without touching history -- comparing is the job, and a reload
// would reset the scroll position that makes two treatments comparable):
//   ?view=ambient                the brief conversation, current treatment
//   ?view=ambient&t=a|b|c        one treatment
//   ?view=ambient&conv=trace     the long one, with code and a stack trace
//   ?view=ambient&bare=1         no picker, for the capture script

const noop = () => {};

export const TREATMENTS = [
  { id: "current", label: "Actual", cap: "velo 0.78 uniforme · 4 manchas + blur · superficies iguales" },
  { id: "a", label: "A · Baño", cap: "velo 0.40 · luz quieta desde fuera del marco · profundidad por tono" },
  { id: "b", label: "B · Horizonte", cap: "sin velo · luz en los dos horizontes · profundidad por borde y halo" },
  { id: "c", label: "C · Deriva", cap: "velo en degradado 0.30→0.62→0.25 · luz que deriva · profundidad por sombra" },
];

/* ── Fixtures ──────────────────────────────────────────────────────────────
   Raw server-shaped messages, same shape as tool-conversations.js, so every
   row is derived by the shipped code. Stamped relative to now so the hours in
   the gutter are today's. */

const T0 = Date.now() - 23 * 60_000;
const at = (min) => T0 + min * 60_000;

const user = (id, text, min) => ({
  role: "user",
  _msg_id: id,
  msg_id: id,
  timestamp: at(min),
  content: [{ type: "text", text }],
});
const said = (id, text, min) => ({
  role: "assistant",
  _msg_id: id,
  msg_id: id,
  timestamp: at(min),
  content: [{ type: "text", text }],
});
const done = (id, tool_name, args, result) => ({
  _type: "tool_start",
  _msg_id: `m-${id}`,
  msg_id: `m-${id}`,
  tool_call_id: id,
  tool_name,
  args,
  status: "done",
  result,
});
const failed = (id, tool_name, args, result) => ({ ...done(id, tool_name, args, result), status: "error" });
const lines = (n, make) => Array.from({ length: n }, (_, i) => make(i + 1)).join("\n");

/* The owner's screen: a multi-line question, two calls, one answer, and room
   left under it. The empty zone is the point — it is where light can live
   without being read through. */
const BRIEF = {
  id: "amb-brief",
  title: "aspecto ambient",
  messages: [
    user(
      "u1",
      "En el móvil la cabecera, mi mensaje y el composer salen del mismo gris.\nParecen recortes pegados sobre un plano, no capas. ¿Y la aurora? Se ve dónde acaba.",
      0,
    ),
    said("a1", "Miro qué hay entre la aurora y el texto antes de tocar nada.", 1),
    done("g1", "grep", { pattern: "rgba\\(16, 16, 24", path: "src", include: "*.css" }, "layout/mobile/MobileConversationScreen/MobileConversationScreen.css:236\nlayout/ConversationScreen/ConversationScreen.css:114\nlayout/Composer/Composer.css:34"),
    done("r1", "read", { path: "src/tokens/shell.css" }, lines(138, (i) => `line ${i}`)),
    said(
      "a2",
      "La aurora no es floja: está tapada. `.mconv` pinta un velo del canvas al 78 % sobre la zona de lectura, así que sólo asoma por los bordes, y ahí se ve dónde acaba cada mancha.",
      2,
    ),
  ],
};

/* The stress case: code and a stack trace, read on top of whatever the
   treatment paints. A drifting light is pleasant until this is on screen. */
const TRACE = {
  id: "amb-trace",
  title: "ws resume race",
  messages: [
    user("u1", "Corre los tests del bus con -race y dime por qué falla el de resume.", 0),
    said("a1", "Los lanzo y miro el orden entre suscripción y snapshot.", 1),
    failed("b1", "bash", { command: "go test -race -count=5 -run TestResume ./pkg/serve/" }, "--- FAIL: TestResumeDoesNotDropEventsPublishedDuringSnapshot (0.31s)\n    resume_race_test.go:41: event 7 never arrived\n    resume_race_test.go:44: got seq [1 2 3 4 5 6 8 9], want 9 events\nFAIL\nexit status 1"),
    done("r1", "read", { path: "pkg/serve/ws.go" }, lines(232, (i) => `line ${i}`)),
    done("g1", "grep", { pattern: "Subscribe\\(", path: "pkg/bus" }, "pkg/bus/bus.go:41\npkg/serve/ws.go:262\npkg/serve/resume.go:88"),
    said(
      "a2",
      "El snapshot se toma **antes** de suscribirse al bus, así que lo publicado entre las dos líneas no está en el snapshot ni llega por el canal:\n\n```go\nfunc (c *client) resume(ctx context.Context, sess *session.Session, from int) error {\n\tsnap, last := sess.Log.Snapshot(from)   // 1. copia hasta `last`\n\tch := sess.Bus.Subscribe(c.id)          // 2. el evento 7 ya pasó\n\tc.forward(ch, func(e Event) bool {\n\t\treturn e.Seq > last\n\t})\n\treturn c.send(snap)\n}\n```\n\nCon `-race` no salta porque no es una carrera de memoria, es una ventana lógica. La traza del fallo es la de siempre:\n\n```text\npanic: test timed out after 30s\n\ngoroutine 42 [chan receive]:\n  github.com/e-aleixandre/moa/pkg/serve.(*client).forward(0xc0001a2000, ...)\n      pkg/serve/ws.go:311 +0x1f4\n  github.com/e-aleixandre/moa/pkg/serve.(*client).resume(0xc0001a2000, ...)\n      pkg/serve/ws.go:266 +0x2c8\n  github.com/e-aleixandre/moa/pkg/serve.TestResumeDoesNotDropEvents.func1()\n      pkg/serve/resume_race_test.go:38 +0x9a\n```\n\nEl arreglo es invertir las dos líneas y filtrar por `Seq > last`, que ya está.",
      3,
    ),
    user("u2", "Vale. Antes de tocarlo, ¿hay más sitios con el mismo orden?", 5),
    done("g2", "grep", { pattern: "Snapshot\\(", path: "pkg", include: "*.go" }, "pkg/session/log.go:140\npkg/serve/ws.go:262\npkg/serve/resume.go:88\npkg/serve/handlers.go:212"),
    done("r2", "read", { path: "pkg/serve/handlers.go" }, lines(310, (i) => `line ${i}`)),
    said("a3", "Uno más: `pkg/serve/resume.go:88` hace lo mismo. `handlers.go:212` suscribe primero, ese está bien.", 6),
  ],
};

const CONVS = {
  brief: { id: "brief", label: "Breve", session: BRIEF },
  trace: { id: "trace", label: "Traza", session: TRACE },
};

const STATUS = {
  ctxPercent: 41,
  tokensUp: 8400,
  tokensDown: 1200,
  spend: "$0.62",
  session: { permissionMode: "yolo" },
  modelName: "Daybreak Blue",
  thinking: "medium",
};

function readParams() {
  const p = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
  const t = TREATMENTS.some((x) => x.id === p.get("t")) ? p.get("t") : "current";
  const conv = CONVS[p.get("conv")] ? p.get("conv") : "brief";
  // ?bare=1 — the phone alone, for the capture script.
  return { t, conv, bare: p.get("bare") === "1" };
}

/* The light layer. One node per treatment that needs more than a background:
   C animates two children on `transform` only, which stays on the compositor
   — a blurred `filter` would be re-rasterised every frame. */
function Light({ t }) {
  if (t === "current") return null;
  return (
    <div class="amb-light" aria-hidden="true">
      {t === "c" && (
        <>
          <div class="amb-drift amb-drift-1" />
          <div class="amb-drift amb-drift-2" />
        </>
      )}
    </div>
  );
}

function Screen({ conv }) {
  const blocks = projectStream(conv.session);
  const hostRef = useRef(null);

  // Open at the top: the brief conversation is read from its first line, and
  // the empty zone under it is what the treatments are judged on. The stress
  // case is scrolled by hand, which is the point of it.
  useLayoutEffect(() => {
    const el = hostRef.current?.querySelector(".zl-transcript");
    if (el) el.scrollTop = 0;
  }, [conv.id]);

  return (
    <div class="mconv amb-screen" ref={hostRef}>
      <MobileStream session={conv.session} blocks={blocks} onOpenSubagent={noop} />
      <MobileChrome title={conv.session.title} onToggle={noop} onNew={noop} />
      <div class="zl-dock">
        <Composer compact />
        <StatusStrip
          compact
          ctxPercent={STATUS.ctxPercent}
          tokensUp={STATUS.tokensUp}
          tokensDown={STATUS.tokensDown}
          spend={STATUS.spend}
          session={STATUS.session}
          onOpenUsage={noop}
          onOpenMcp={noop}
          onPerm={noop}
          showTokens
          modelName={STATUS.modelName}
          thinking={STATUS.thinking}
          thinkingPosition={STATUS.thinking}
          onModel={noop}
        />
      </div>
    </div>
  );
}

export function AmbientLab() {
  const initial = readParams();
  const [t, setT] = useState(initial.t);
  const [convId, setConvId] = useState(initial.conv);
  const conv = CONVS[convId];
  const treatment = TREATMENTS.find((x) => x.id === t);

  return (
    <div class={`amb${initial.bare ? " is-bare" : ""}`} data-t={t}>
      <div class="amb-stage">
        <Light t={t} />
        <Screen conv={conv} />
      </div>
      <nav class="amb-bar" aria-label="Treatment">
        <p class="amb-cap"><b>{treatment.label}</b> — {treatment.cap}</p>
        <div class="amb-row">
          <div class="amb-seg" role="group" aria-label="Light and depth">
            {TREATMENTS.map((x) => (
              <button
                type="button"
                key={x.id}
                class={x.id === t ? "is-on" : ""}
                aria-pressed={x.id === t}
                onClick={() => setT(x.id)}
              >
                {x.id === "current" ? x.label : x.id.toUpperCase()}
              </button>
            ))}
          </div>
          <div class="amb-seg amb-seg-conv" role="group" aria-label="Conversation">
            {Object.values(CONVS).map((c) => (
              <button
                type="button"
                key={c.id}
                class={c.id === convId ? "is-on" : ""}
                aria-pressed={c.id === convId}
                onClick={() => setConvId(c.id)}
              >
                {c.label}
              </button>
            ))}
          </div>
        </div>
      </nav>
    </div>
  );
}
