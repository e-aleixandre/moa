// tool-conversations — CATALOG ONLY. Fixtures for the tool-call review.
//
// These are RAW SERVER-SHAPED messages, not ledger props: `_type:'tool_start'`
// with `tool_name` / `args` / `status` / `result`, exactly what the WS sends.
// tools-lab feeds them to the production `projectStream`, so every row on
// screen was derived by the shipped code (toolPath, toolPreview, deriveOut,
// mapStatus, fuseLedgerDetails) rather than hand-written to look right. A
// fixture that produced a prettier row than production would be worthless.
//
// Four conversations instead of one: a single transcript covering every tool
// and every state stops reading like work and becomes a catalogue in disguise.
// Each one is a plausible session with its own problem, and between them they
// cover the whole matrix (see COVERAGE at the bottom).

// The transcript is read in the present, so the fixtures are stamped relative
// to now: a hard-coded epoch would paint yesterday's hours on every turn.
const T0 = Date.now() - 52 * 60_000;
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

// tool — one terminated call. `status` is the server's vocabulary
// ('done' | 'error' | 'rejected'), which mapStatus collapses to ok/err/warn.
const tool = (id, tool_name, args, status, result, extra = {}) => ({
  _type: "tool_start",
  _msg_id: `m-${id}`,
  msg_id: `m-${id}`,
  tool_call_id: id,
  tool_name,
  args,
  status,
  result,
  ...extra,
});

const done = (id, name, args, result, extra) => tool(id, name, args, "done", result, extra);
const failed = (id, name, args, result, extra) => tool(id, name, args, "error", result, extra);
const refused = (id, name, args, result, extra) => tool(id, name, args, "rejected", result, extra);

// A live row is only ever the LAST message: projectStream marks the trailing
// running tool, and only that one (stream-model.js:490). So each conversation
// gets at most one, and the three of them carry different live shapes.
const running = (id, name, args, extra) => tool(id, name, args, "running", "", { startedAt: Date.now() - 9000, ...extra });
const generating = (id, name, args, extra) => tool(id, name, args, "generating", "", { startedAt: Date.now() - 4000, ...extra });

const lines = (n, make) => Array.from({ length: n }, (_, i) => make(i + 1)).join("\n");

/* ── 1 · Reading and searching ─────────────────────────────────────────────
   The reconnaissance half of a real bug hunt: find the code, read it, consult
   the docs and memory. This is the conversation that carries the LONG group
   (12 calls), which is where the fold header appears. */

const LONG_HUNT = [
  done("h1", "grep", { pattern: "Subscribe\\(", path: "pkg/bus" }, "pkg/bus/bus.go:41:func (b *Bus) Subscribe(id string) <-chan Event\npkg/bus/bus.go:77\npkg/serve/ws.go:262\npkg/serve/ws.go:311\npkg/serve/resume.go:88\npkg/session/log.go:140\npkg/session/log.go:203"),
  done("h2", "find", { glob: "**/*_test.go", path: "pkg/bus", type: "f" }, "pkg/bus/bus_test.go\npkg/bus/fanout_test.go"),
  done("h3", "read", { path: "pkg/bus/bus.go" }, lines(184, (i) => `line ${i}`)),
  done("h4", "read", { path: "pkg/serve/ws.go" }, lines(232, (i) => `line ${i}`)),
  done("h5", "ls", { path: "pkg/serve" }, "ws.go\nresume.go\nhandlers.go\nconversation.go\nstatic/\nfrontend/"),
  done("h6", "grep", { pattern: "Snapshot\\(", path: "pkg/session", include: "*.go" }, "pkg/session/log.go:140\npkg/session/log.go:203\npkg/session/log_test.go:55"),
  done("h7", "read", { path: "pkg/session/log.go" }, lines(210, (i) => `line ${i}`)),
  // One-line output: the row's `out` is the whole result, not a count. Worth
  // looking at next to "232 lines" — they occupy the same slot.
  done("h8", "bash", { command: "git log --oneline -1 pkg/serve/ws.go" }, "2de38dd fix(serve): resubscribe before snapshot"),
  done("h9", "memory", { action: "search", query: "ws resume race" }, "project/openai-stall-rootcause\nproject/no-hay-tests-e2e"),
  done("h10", "moa_docs", { page: "architecture" }, lines(96, (i) => `doc line ${i}`)),
  done("h11", "grep", { pattern: "forward\\(", path: "pkg/serve" }, "pkg/serve/ws.go:311\npkg/serve/ws.go:344"),
  done("h12", "read", { path: "pkg/serve/resume.go" }, lines(128, (i) => `line ${i}`)),
];

export const READING = {
  id: "conv-reading",
  title: "ws resume race",
  messages: [
    user(
      "u1",
      "En el móvil, cuando vuelvo a una sesión que estuvo un rato sin socket, a veces faltan los últimos mensajes hasta que recargo. ¿Puedes mirar de dónde sale?",
      0,
    ),
    said(
      "a1",
      "Voy a mirar el camino de resume: quién se suscribe al bus y en qué orden se toma el snapshot. Si la suscripción llega después del snapshot hay una ventana en la que los eventos se pierden.",
      1,
    ),
    ...LONG_HUNT,
    said(
      "a2",
      "Ya lo tengo localizado. En `pkg/serve/ws.go:262` el snapshot se toma **antes** de suscribirse al bus, así que todo lo que se publique entre esas dos líneas no lo ve nadie: no está en el snapshot y todavía no hay canal. Es una ventana pequeña, de ahí que solo se note cuando la sesión estaba trabajando.\n\nLos tests de `pkg/bus` no lo cogen porque suscriben primero.",
      4,
    ),
    user("u2", "¿Y la parte del cliente? El móvil pinta el transcript cacheado mientras tanto, ¿no?", 5),
    // A short group: the common case, two or three calls between two remarks.
    done("s1", "read", { path: "pkg/serve/frontend/src/layout/mobile/MobileConversationScreen/MobileConversationScreen.jsx" }, lines(148, (i) => `line ${i}`)),
    done("s2", "grep", { pattern: "historyPending", path: "pkg/serve/frontend/src", include: "*.js*" }, "data/stream-model.js:41\ncomponents/HistoryHydrationTail/HistoryHydrationTail.jsx:12\nlayout/Stream/ConversationStream.jsx:196"),
    said(
      "a3",
      "Sí. El cliente monta `MobileStream` sin estado de carga, así que durante esos segundos estás viendo la caché de la última visita sin ninguna señal de que está desfasada. Son dos fallos distintos que se suman: el servidor pierde eventos y el cliente no dice que va con retraso.",
      6,
    ),
    // MCP and the generic unknown tool, in the place they'd really appear.
    done("s3", "mcp__linear__create_issue", { title: "ws resume: subscribe before snapshot", team: "core" }, "Created CORE-418"),
    done("s4", "pulse_deploy_status", { env: "demo" }, "demo: healthy (build 4412)"),
    said("a4", "Abierto CORE-418 con el diagnóstico y las dos piezas separadas.", 7),
  ],
};

/* ── 2 · Editing and writing ───────────────────────────────────────────────
   The fix for what conversation 1 found. Carries the edit family (edit,
   multiedit, write, apply_patch), the tools with dedicated detail panels
   (ask_user, send_file, tasks), a subagent, and ends GENERATING — a live edit
   streaming its diff preview. */

const WS_DIFF = `--- a/pkg/serve/ws.go
+++ b/pkg/serve/ws.go
@@ -259,8 +259,9 @@ func (c *client) resume(ctx context.Context, sess *session.Session, from int) err
-	snap, last := sess.Log.Snapshot(from)
-	ch := sess.Bus.Subscribe(c.id)
+	// Subscribe BEFORE snapshotting: an event published between the two is
+	// in neither the snapshot nor the channel, and is lost for good.
+	ch := sess.Bus.Subscribe(c.id)
+	snap, last := sess.Log.Snapshot(from)
 	c.forward(ch, func(e Event) bool {
 		return e.Seq > last
 	})`;

const LIVE_DIFF = `--- a/pkg/serve/frontend/src/layout/mobile/MobileConversationScreen/MobileStream.jsx
+++ b/pkg/serve/frontend/src/layout/mobile/MobileConversationScreen/MobileStream.jsx
@@ -1,5 +1,9 @@
 export function MobileStream(props) {
-  return <ConversationStream {...props} visibleDone={1} />;
+  return (
+    <ConversationStream
+      {...props}
+      visibleDone={1}`;

export const EDITING = {
  id: "conv-editing",
  title: "fix resume window",
  messages: [
    user("u1", "Vale, arréglalo. Primero el servidor, que es el que pierde datos.", 0),
    said(
      "a2",
      "Voy por partes: primero el orden en `ws.go`, luego un test que falle sin el arreglo, y al final la señal de carga en el cliente.",
      1,
    ),
    done("t1", "tasks", { action: "create", title: "Subscribe before snapshot on resume" }, "Created task 1"),
    done("t2", "tasks", { action: "create", title: "Regression test for the resume window" }, "Created task 2"),
    // The edit whose server diff opens INSIDE the row (fuseLedgerDetails).
    done("e1", "edit", { path: "pkg/serve/ws.go" }, WS_DIFF),
    done("e2", "read", { path: "pkg/bus/bus_test.go" }, lines(88, (i) => `line ${i}`)),
    done("e3", "write", { path: "pkg/serve/resume_race_test.go", content: "package serve\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\n// TestResumeDoesNotDropEventsPublishedDuringSnapshot pins the window: the\n// event is published while the snapshot is being taken, so a client that\n// subscribes afterwards never sees it.\nfunc TestResumeDoesNotDropEventsPublishedDuringSnapshot(t *testing.T) {\n\tt.Parallel()\n}\n" }, "wrote 14 lines"),
    done("e4", "bash", { command: "go test -race -count=20 -run TestResumeDoesNotDropEvents ./pkg/serve/" }, "--- FAIL: TestResumeDoesNotDropEventsPublishedDuringSnapshot (0.31s)\n    resume_race_test.go:41: event 7 never arrived\nFAIL\nexit status 1"),
    said("a3", "El test falla sin el arreglo, que es lo que quería. Ahora lo aplico.", 3),
    done("e5", "multiedit", { path: "pkg/serve/ws.go", edits: [{ oldText: "snap, last := sess.Log.Snapshot(from)", newText: "ch := sess.Bus.Subscribe(c.id)" }, { oldText: "ch := sess.Bus.Subscribe(c.id)", newText: "snap, last := sess.Log.Snapshot(from)" }] }, WS_DIFF),
    done("e6", "apply_patch", { patch: "*** Begin Patch\n*** Update File: pkg/serve/resume.go\n@@\n-\tsnap := sess.Log.Snapshot(from)\n+\tsnap := sess.Log.Snapshot(from) // after Subscribe\n*** End Patch" }, "Applied to 1 file"),
    done("e7", "bash", { command: "go test -race -count=20 -run TestResumeDoesNotDropEvents ./pkg/serve/" }, "ok  \tgithub.com/e-aleixandre/moa/pkg/serve\t4.117s"),
    done("e8", "verify", {}, "build: ok\ntest: 1622 pass / 0 fail\nlint: ok"),
    said(
      "a4",
      "Servidor arreglado y con test de regresión. Para el cliente hay una decisión que no me toca a mí.",
      4,
    ),
    // ask_user gets its own detail renderer (AskUserDetail), not a code block.
    done(
      "q1",
      "ask_user",
      {
        questions: [
          {
            question: "Mientras llega el snapshot, ¿qué prefieres ver en el móvil?",
            options: [
              { label: "El transcript cacheado con un aviso" },
              { label: "Un esqueleto al final del scroll" },
              { label: "La conversación atenuada" },
            ],
          },
        ],
      },
      "Un esqueleto al final del scroll",
    ),
    user("u2", "Eso, el esqueleto al final. Y pásame el diff completo cuando acabes.", 5),
    // A subagent call and the file card it produced.
    done("g1", "subagent", { task: "Revisar el arreglo de ws.go buscando otras ventanas entre Subscribe y Snapshot en el resto de handlers", model: "sol", thinking: "high" }, "Sin más ventanas. `pkg/serve/handlers.go:212` suscribe primero, correcto."),
    done("f1", "send_file", { path: "/tmp/resume-window.diff" }, 'Sent.\n{"name":"resume-window.diff","size":2841,"mime":"text/x-diff","url":"/api/files/resume-window.diff","title":"resume window fix"}'),
    done("k1", "checkpoint", { label: "before mobile skeleton" }, "Checkpoint saved"),
    said("a5", "Diff enviado. Ahora el esqueleto en el cliente.", 6),
    // GENERATING: the live row streams the diff it is producing.
    generating("live-edit", "edit", { path: "pkg/serve/frontend/src/layout/mobile/MobileConversationScreen/MobileStream.jsx" }, { result: LIVE_DIFF }),
  ],
};

/* ── 3 · Failures and extremes ─────────────────────────────────────────────
   Deploy gone wrong. This is where the ugly content lives: the 2000-line
   output, the enormous command, the 180-char path, the empty result, the
   rejected call and a mixed group with an error in the middle. Ends RUNNING,
   with a bash streaming its tail. */

const HUGE_CMD =
  "cd /home/ealeixandre/dev/moa/design-visual && GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w -X main.version=0.37.4 -X main.commit=2de38dd -X main.date=2026-09-10T11:04:00Z' -o /tmp/moa-linux-amd64 ./cmd/moa && scp -o StrictHostKeyChecking=no /tmp/moa-linux-amd64 box:/usr/local/bin/moa.new && ssh box 'sudo install -m 0755 /usr/local/bin/moa.new /usr/local/bin/moa && sudo systemctl restart moa && sleep 3 && systemctl is-active moa'";

const HUGE_PATH =
  "pkg/serve/frontend/src/layout/mobile/MobileConversationScreen/components/transcript/hydration/MobileHydrationBoundaryController.jsx";

export const FAILING = {
  id: "conv-failing",
  title: "deploy pulse-api",
  messages: [
    user("u1", "El deploy de pulse-api se ha quedado a medias y /healthz da 502. Mira qué ha pasado.", 0),
    said("a1", "Miro el estado del servicio y los logs desde el último arranque.", 1),
    // Mixed group with the failure in the middle: the case the owner sees most.
    done("d1", "bash", { command: "ssh box 'systemctl is-active pulse-api'" }, "activating"),
    failed("d2", "bash", { command: "curl -sS --max-time 5 -o /dev/null -w '%{http_code}' https://box.internal:8081/healthz" }, "curl: (28) Operation timed out after 5001 milliseconds\nexit status 28"),
    done("d3", "bash", { command: "ssh box 'journalctl --user -u pulse-api -n 2000 --no-pager'" }, lines(2000, (i) => `sep 10 11:0${i % 6}:${String(i % 60).padStart(2, "0")} box pulse-api[4412]: worker ${i % 8} · request ${i} · 200 · 3.1ms`)),
    // Empty result: done, nothing to show. The row has no detail at all.
    done("d4", "bash", { command: "ssh box 'test -S /run/pulse-api.sock && echo yes'" }, ""),
    done("d5", "read", { path: HUGE_PATH }, lines(64, (i) => `line ${i}`)),
    said(
      "a2",
      "El servicio se queda en `activating`: arranca, no consigue abrir el socket y systemd lo reintenta. En los logs hay 2000 líneas de peticiones viejas y ninguna del arranque nuevo, así que el proceso nuevo ni llega a escuchar.",
      3,
    ),
    // The enormous command, and a rejected call right after it.
    failed("d6", "bash", { command: HUGE_CMD }, "ssh: connect to host box port 22: Connection refused\nexit status 255"),
    refused("d7", "bash", { command: "ssh box 'sudo systemctl stop pulse-api && sudo rm -rf /var/lib/pulse-api/state'" }, "Rejected by the user"),
    said(
      "a3",
      "He pedido borrar el estado y has dicho que no — bien, era destructivo y no está demostrado que sea eso. Sin tocar nada, lo que sí puedo hacer es mirar la configuración del socket.",
      4,
    ),
    done("d8", "fetch_content", { url: "https://www.freedesktop.org/software/systemd/man/systemd.socket.html" }, lines(140, (i) => `man line ${i}`)),
    done("d9", "web_search", { query: "systemd service stuck activating socket already in use" }, "3 results"),
    failed("d10", "read", { path: "/etc/pulse-api/socket.conf" }, "open /etc/pulse-api/socket.conf: no such file or directory"),
    done("d11", "load_skill", { name: "release-preflight" }, "Skill loaded"),
    said("a4", "Voy a reproducirlo con el servicio en primer plano para ver el error real del bind.", 5),
    // RUNNING: bash streaming its tail into the mini log window.
    running("live-bash", "bash", { command: "ssh box 'sudo -u pulse /usr/local/bin/pulse-api --config /etc/pulse-api/config.toml --log-level=debug'" }, {
      streamingResult:
        "2026-09-10T11:41:02Z INF loading config from /etc/pulse-api/config.toml\n2026-09-10T11:41:02Z INF 14 routes registered\n2026-09-10T11:41:02Z DBG opening unix socket /run/pulse-api.sock\n2026-09-10T11:41:02Z WRN socket exists, unlinking stale file\n2026-09-10T11:41:03Z DBG binding 0.0.0.0:8081\n",
    }),
  ],
};

/* ── 4 · The owner's own tools ─────────────────────────────────────────────
   An owner working its project: it records a decision in the book, starts a
   session to act on it, and corrects that session a minute later. These are
   the calls that used to read "wrote areas/x.md" and "Sent to 66f1c2… ()" —
   the book write opens its content like a file write, the append shows the
   inserted lines, and each message to a session names the session and shows
   what it said. */

const FICHA = `# Albaranes

Un albarán por pedido, nunca agrupados: el cliente factura contra el albarán y
agrupar dos pedidos le rompe la conciliación.

## Quién lo pidió
Winerim (marzo). La decisión se tomó con ellos delante, no es una suposición
nuestra.

## Lo que NO se hace
- No se agrupan albaranes de pedidos distintos.
- No se borra un albarán emitido: se emite uno de abono.`;

export const OWNING = {
  id: "conv-owning",
  title: "erp · albaranes",
  messages: [
    user("u1", "Winerim ha confirmado que los albaranes no se agrupan nunca. Apúntalo y que alguien lo arregle en el ERP.", 0),
    said("a1", "Lo dejo escrito en el libro primero, que es donde lo va a leer quien toque el módulo, y luego abro una sesión para el arreglo.", 1),
    done("bw1", "book", { action: "write", path: "areas/erp/albaranes.md", content: FICHA }, "Wrote areas/erp/albaranes.md in the book."),
    done("ba1", "book", { action: "append", path: "decisiones.md", content: "\n## 2026-09-19 · Albaranes sin agrupar\nConfirmado por Winerim. Detalle en areas/erp/albaranes.md." }, "Appended decisiones.md in the book."),
    done("bl1", "book", { action: "list", path: "areas/erp" }, "areas/erp/albaranes.md\nareas/erp/facturacion.md"),
    said("a2", "Apuntado. Ahora la sesión que lo arregla.", 2),
    done("sn1", "sessions", { action: "new", text: "En `internal/erp/albaran.go` hay un camino que agrupa pedidos del mismo cliente en un albarán. **No debe existir**: un albarán por pedido.\n\nMira primero `areas/erp/albaranes.md` en el libro, que explica por qué.", cwd: "/home/dev/erp/internal/erp", model: "terra", thinking: "medium" }, "Started session sqlite in /home/dev/erp/internal/erp."),
    done("sl1", "sessions", { action: "list" }, "sqlite — migrate sqlite · running\nfrontend — frontend polish · idle"),
    said("a3", "Está trabajando. Le falta un dato que sí está en el libro, se lo paso.", 3),
    done("ss1", "sessions", { action: "send", session_id: "sqlite", text: "Un apunte: los albaranes ya emitidos **no se borran**, se emite uno de abono. Si el arreglo toca el borrado, párate y dímelo." }, "Queued for sqlite (it is working; it will read this at its next step)."),
    done("sa1", "sessions", { action: "answer", session_id: "sqlite", ask_id: "ask-7d2", answers: ["Sí, uno por pedido siempre", "No toques la numeración"] }, "Answered ask-7d2 in session sqlite."),
    said("a4", "Le he contestado yo: las dos preguntas las responde el libro, no hacía falta molestarte.", 4),
    // What comes back the other way: a session's report. It must name the
    // session the way "sent to" does, so both directions read alike.
    {
      role: "user",
      _msg_id: "rp1",
      msg_id: "rp1",
      timestamp: at(9),
      content: [{ type: "text", text: "sqlite: done. Un albarán por pedido; el camino que agrupaba está borrado y los emitidos siguen intactos." }],
      custom: { source: "report", count: 1, sessions: [{ id: "sqlite", title: "migrate sqlite", status: "done" }] },
    },
  ],
};

export const CONVERSATIONS = [
  {
    id: "reading",
    session: READING,
    label: "Reading and searching",
    note: "The reconnaissance half of a bug hunt. Carries the 12-call group (the fold header), a short two-call group, one-line output, MCP and an unknown tool.",
  },
  {
    id: "editing",
    session: EDITING,
    label: "Editing and writing",
    note: "The fix. The edit family with diffs opening inside the row, the tools with their own detail panel (ask_user, send_file), a subagent, and a live GENERATING edit streaming its diff.",
  },
  {
    id: "owning",
    session: OWNING,
    label: "The owner's tools",
    note: "An owner recording a decision and directing a session: book write/append opening like a file change, and each message to a session naming its target and showing what was said.",
  },
  {
    id: "failing",
    session: FAILING,
    label: "Failures and extremes",
    note: "A deploy gone wrong: error and rejected in a mixed group, 2000-line output, a 350-char command, a 150-char path, an empty result, and a live RUNNING bash streaming its tail.",
  },
];

/* COVERAGE — what the three conversations exercise, so a gap is visible here
   rather than discovered in a screenshot.

   Tools ..... book (write/append/list) sessions (new/send/answer/list)
               read ls grep find bash edit multiedit write apply_patch
               fetch_content web_search tasks memory verify moa_docs send_file
               subagent ask_user load_skill checkpoint mcp__linear__create_issue
               pulse_deploy_status (unknown → generic `tool` icon)
   States .... done (ok) · error (err) · rejected (warn) · running · generating
   Groups .... 2-call, 4-call, mixed-with-error, and 12-call (folded)
   Extremes .. 2000-line output · 350-char command · 150-char path
               one-line output · empty result · unknown tool                  */
