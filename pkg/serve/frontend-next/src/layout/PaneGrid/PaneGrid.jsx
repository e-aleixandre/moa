import { Plus } from "lucide-preact";
import { Pane } from "../Pane/Pane.jsx";
import { ActivityLedger, CodeBlock, PermissionCard } from "../../components/index.js";
import "./PaneGrid.css";

// Contenido mock de cada pane, fiel a panes-desktop.html (textos en inglés).

const WS_RACE_LEDGER = [
  { tool: "read", arg: { text: "pkg/serve/ws.go", detail: "210–340" }, out: "130 ln", status: "ok" },
  { tool: "edit", arg: { text: "pkg/serve/ws.go", detail: "resume()" }, out: "+5 −3", status: "ok" },
  { tool: "bash", arg: "go test -race ./... — full suite", out: "running 0:41", status: "live" },
];

const WS_RACE_CODE = `// resume: subscribe BEFORE snapshot
ch := sess.Bus.Subscribe(c.id)
snap, last := sess.Log.Snapshot(from)
c.sendAll(snap)
c.forward(ch, func(e Event) bool { return e.Seq > last })`;

function WsRaceFixBody() {
  return (
    <>
      <div class="u-line">
        <span class="w">You</span>
        Fix the reconnect race in ws.go, with a regression test.
      </div>
      <p>
        Found it — <code>Subscribe</code> registers after the snapshot read
        releases its lock, so events in that window are lost. Reordering and
        draining duplicates by <code>seq</code>:
      </p>
      <ActivityLedger className="mini-ledger" rows={WS_RACE_LEDGER} />
      <CodeBlock className="mini-code" code={WS_RACE_CODE} lang="go" showHeader={false} />
      <p>Stress test passes 50/50 under <code>-race</code>. Full suite running now…</p>
    </>
  );
}

function DeployPulseApiBody() {
  return (
    <>
      <p>
        Build is green and the systemd unit is staged. I need your go-ahead
        to restart the service:
      </p>
      <PermissionCard
        title="moa wants to run"
        command="systemctl --user restart pulse-api"
      />
    </>
  );
}

function MigrateSqliteBody() {
  return (
    <>
      <div class="err-note">
        <span class="mono">
          provider: 429 rate_limited — retrying in 34s (attempt 3/5)
        </span>
        <button type="button" class="retry">retry now</button>
      </div>
      <p class="dim-p">
        Paused mid-migration. Schema v7 applied, backfill pending.
      </p>
    </>
  );
}

// PaneGrid — contenedor de la rejilla de panes. Reproduce el layout del
// mockup: split-x vertical (hint de resize, sin lógica real todavía), un
// pane "tall" focused a la izquierda, y una columna derecha con permiso +
// una fila inferior (error + ghost).
export function PaneGrid({ onAddPane }) {
  return (
    <div class="pane-grid">
      {/* TODO(fase-resize): el split-x es solo un hint visual (resalta al
          hover); el resize real de columnas/filas llega en una fase
          posterior con estado de layout persistido. */}
      <div class="split-x" title="Drag to resize" aria-hidden="true" />

      <Pane
        variant="tall"
        focused
        title="ws race fix"
        path="~/dev/moa/main"
        state="running"
        model="sol"
        thinkingLevel="medium"
      >
        <WsRaceFixBody />
      </Pane>

      <Pane
        title="deploy pulse api"
        path="~/dev/moa/pulse-api"
        state="permission"
        titleTone="yellow"
      >
        <DeployPulseApiBody />
      </Pane>

      <div class="pane-grid-row">
        <Pane title="migrate sqlite" state="error">
          <MigrateSqliteBody />
        </Pane>

        <button type="button" class="ghost" onClick={onAddPane}>
          <span class="big" aria-hidden="true"><Plus size={22} /></span>
          <span class="ghost-label">Add a pane here</span>
          <span class="hint">drag a session from the spine, or ⌘K → ⌘⏎</span>
        </button>
      </div>
    </div>
  );
}
