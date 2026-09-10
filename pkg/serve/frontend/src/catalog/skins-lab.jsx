import { useState } from "preact/hooks";
import { AppWindow } from "lucide-preact";
import { Spine } from "../layout/Spine/Spine.jsx";
import { ChatHead } from "../layout/ChatHead/ChatHead.jsx";
import { Composer } from "../layout/Composer/Composer.jsx";
import { StatusStrip } from "../layout/StatusStrip/StatusStrip.jsx";
import { LiveBar } from "../layout/LiveBar/LiveBar.jsx";
import { MobileTitleChip } from "../layout/mobile/MobileTitleChip/MobileTitleChip.jsx";
import { SessionDrawer } from "../layout/mobile/SessionDrawer/SessionDrawer.jsx";
import { UserWaypoint, AssistantDocument, ActivityLedger } from "../components/index.js";
import { drawerSessions, drawerProjects } from "../layout/mobile/MobileConversationScreen/chrome.js";
import { spineSessions } from "../layout/Spine/sessions.js";
import { sessionTitle, shortPath } from "../data/util/format.js";
import { ACTIVE_ID, REDESIGN_SESSIONS } from "./redesign-fixtures.js";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "../layout/mobile/MobileConversationScreen/MobileStream.css";
import "./skins-lab.css";

// skins-lab — the STYLE axis only, with the screen architecture frozen.
//
// Every frame here is built from the production components (Spine, ChatHead,
// Composer, StatusStrip, LiveBar, SessionDrawer, MobileTitleChip, the stream
// blocks) fed by the redesign fixtures through the real selectors. A skin is a
// class on the frame (.sk-<key>) and a block of CSS overrides in
// skins-lab.css: nothing moves, nothing is regrouped, no control is added or
// removed. If a skin needed different markup to look right, it would be
// crossing into architecture — which is exactly what this lab is not for.
//
// .sk-terminal has no rules and IS the baseline.

const noop = () => {};

export const SKINS = [
  {
    key: "ambient",
    label: "Ambient",
    sub: "El color vive en el ambiente: aurora de fondo, paneles de vidrio con blur real, superficies separadas por luminosidad y no por bordes, radios generosos, menos mono. Los controles son acromáticos, así que los puntos de estado son lo único saturado de la pantalla.",
  },
];

// Real selectors, real fixtures — the same roster on both densities.
const DRAWER = drawerSessions(REDESIGN_SESSIONS, ACTIVE_ID);
const PROJECTS = drawerProjects(REDESIGN_SESSIONS);
const SPINE = spineSessions(REDESIGN_SESSIONS);
const INBOX_COUNT = 2;
const INBOX = Array.from({ length: INBOX_COUNT }, (_, i) => ({ pending: true, event: { id: `sk-ev-${i}` } }));
const VERSION = { current: "v0.37.2" };
const ACTIVE = REDESIGN_SESSIONS[ACTIVE_ID];
const ACTIVE_TITLE = sessionTitle(ACTIVE);
const ACTIVE_PATH = shortPath(ACTIVE.cwd);

// The focused conversation is the fixture's running session, so the composer
// is in its busy shape (steer + stop), the now-line is on, and the strip has
// telemetry to show. Enough of a session for the presentational pieces; the
// Composer's actions are never triggered here.
const RUN_SESSION = {
  id: ACTIVE_ID,
  state: "running",
  runStartedAtMs: ACTIVE.runStartedAtMs,
  pendingSteers: [],
  permissionMode: "yolo",
  provider: "anthropic",
  model: "anthropic/claude-opus-4-8",
  costUSD: 1.42,
  contextPercent: 38,
  runTokensUp: 12400,
  runTokensDown: 3100,
};

const LEDGER_ROWS = [
  { id: "l1", tool: "read", arg: { text: "src/layout/Spine/Spine.css" }, out: "312 lines", status: "ok" },
  { id: "l2", tool: "grep", arg: { text: '"variant-card" — src/', detail: "5 files" }, out: "23 matches", status: "ok" },
  { id: "l3", tool: "bash", arg: { text: "npm run catalog", detail: "PORT=7322" }, out: "listening", status: "ok" },
  { id: "l4", tool: "write", arg: { text: "src/catalog/skins-lab.css" }, live: true, startedAt: Date.now() - 6000 },
];

function Transcript() {
  return (
    <>
      <UserWaypoint time="10:12">
        <p>Explora solo el eje de estilo, con la arquitectura de pantallas congelada: mismos controles, mismos sitios, solo cambia cómo se ve.</p>
      </UserWaypoint>
      <AssistantDocument>
        <p>Monto los componentes reales y aplico cada piel como una hoja de overrides con ámbito. Así la estructura no puede desviarse: si una piel necesitara otro markup, estaría cruzando la línea.</p>
      </AssistantDocument>
      <ActivityLedger rows={LEDGER_ROWS} />
      <AssistantDocument>
        <p>Primero <strong>ambient</strong>, en las dos densidades y con las 251 guardadas; después la tercera dirección.</p>
      </AssistantDocument>
    </>
  );
}

function Strip({ compact = false }) {
  return (
    <StatusStrip
      compact={compact}
      ctxPercent={RUN_SESSION.contextPercent}
      tokensUp={RUN_SESSION.runTokensUp}
      tokensDown={RUN_SESSION.runTokensDown}
      spend="$1.42"
      session={RUN_SESSION}
      usage={null}
      onOpenUsage={noop}
      onOpenMcp={noop}
      onPermChange={compact ? undefined : noop}
      onPerm={compact ? noop : undefined}
      showTokens
      modelName="Opus"
      modelAccent="lavender"
      thinking="medium"
      onModel={noop}
    />
  );
}

// The phone frame keeps its own drawer state: on mobile the conversation IS
// the screen and the drawer covers it, so judging a skin needs BOTH. The chip
// opens and closes it exactly as in the app. It opens on "pick a session"
// (the task under review); tap the chip to see the composer underneath.
export function SkinPhone({ skin, drawer = false, variant = "" }) {
  const [drawerOpen, setDrawerOpen] = useState(drawer);
  return (
    <div class={`sk-phone mconv sk-${skin}${variant ? ` ${variant}` : ""}`}>
      <div class="sk-aurora" aria-hidden="true" />
      <div class="mstream">
        <div class="mconv-stream">
          <div class="mstream-col">
            <Transcript />
          </div>
        </div>
      </div>
      <div class="mcomposer">
        <Composer
          sessionId={`sk-${skin}-phone`}
          session={RUN_SESSION}
          compact
          plusActions={[{ id: "preview", icon: AppWindow, label: "Live preview", onClick: noop }]}
        />
        <Strip compact />
      </div>
      <MobileTitleChip
        title={ACTIVE_TITLE}
        attention={{ permission: 1, error: 1, urgent: 1 }}
        open={drawerOpen}
        onToggle={setDrawerOpen}
        inboxCount={INBOX_COUNT}
      />
      <SessionDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        newResults={DRAWER.newResults}
        active={DRAWER.active}
        saved={DRAWER.saved}
        activeCount={DRAWER.activeCount}
        savedCount={DRAWER.savedCount}
        projects={PROJECTS}
        inboxVisible
        inboxCount={INBOX_COUNT}
        version={VERSION}
        onSelect={() => setDrawerOpen(false)}
        onCreate={noop}
        onSettings={noop}
        onInbox={noop}
        onCloseSession={noop}
        onReopenSession={noop}
        onDeleteSession={noop}
      />
    </div>
  );
}

export function SkinDesktop({ skin, variant = "" }) {
  return (
    <div class={`sk-desk sk-${skin}${variant ? ` ${variant}` : ""}`}>
      <div class="sk-aurora" aria-hidden="true" />
      <Spine
        version={VERSION}
        activeSessions={SPINE.active}
        savedSessions={SPINE.saved}
        activeId={ACTIVE_ID}
        inbox={INBOX}
        onSelectSession={noop}
        onNewSession={noop}
        onSearch={noop}
        onSettings={noop}
        onCloseSession={noop}
        onReopenSession={noop}
        onDeleteSession={noop}
        onGroupByProject={noop}
        onToggleInbox={noop}
      />
      <div class="conversation-main sk-main">
        <ChatHead title={ACTIVE_TITLE} path={ACTIVE_PATH} onGridToggle={noop} onPreviewToggle={noop} />
        <div class="stream">
          <div class="stream-scroll">
            <div class="stream-col">
              <Transcript />
            </div>
          </div>
        </div>
        <LiveBar session={RUN_SESSION} />
        <Composer sessionId={`sk-${skin}-desk`} session={RUN_SESSION} />
        <div class="status-strip-anchor">
          <Strip />
        </div>
      </div>
    </div>
  );
}

const AURORA_DOSES = [
  { key: "", label: "Plena" },
  { key: "amb-soft", label: "Suave" },
  { key: "amb-trace", label: "Insinuada" },
  { key: "amb-none", label: "Sin aurora" },
];

function SkinRow({ skin }) {
  // Ambient's aurora is a dial, not a constant: the owner found the full dose
  // exaggerated and hard to read, so the row compares the three doses of the
  // SAME skin instead of settling it in words. Defaults to "soft".
  const [dose, setDose] = useState("amb-soft");
  const variant = skin.key === "ambient" ? dose : "";
  return (
    <section class="sk-row" id={`skin-${skin.key}`} data-skin={skin.key}>
      <header class="sk-row-head">
        <h2>{skin.label}</h2>
        <p>{skin.sub}</p>
        {skin.key === "ambient" && (
          <div class="sk-dose" role="group" aria-label="Intensidad del aurora">
            <span class="sk-dose-label">Aurora</span>
            {AURORA_DOSES.map((d) => (
              <button
                key={d.key || "full"}
                type="button"
                class={`sk-dose-btn${dose === d.key ? " is-on" : ""}`}
                aria-pressed={dose === d.key}
                onClick={() => setDose(d.key)}
              >
                {d.label}
              </button>
            ))}
          </div>
        )}
      </header>
      <div class="sk-pair">
        <figure class="sk-figure" data-density="mobile">
          <figcaption class="sk-caption">Móvil · 390×760 · SessionDrawer sobre la conversación</figcaption>
          <SkinPhone skin={skin.key} variant={variant} />
        </figure>
        <figure class="sk-figure" data-density="desktop">
          <figcaption class="sk-caption">Escritorio · 940×640 · Spine + conversación + composer</figcaption>
          <SkinDesktop skin={skin.key} variant={variant} />
        </figure>
      </div>
    </section>
  );
}

export function SkinsLab() {
  return (
    <div class="sk">
      <header class="sk-head">
        <h1>moa studio · <em>ambient</em></h1>
        <p>
          La dirección de estilo elegida, sobre la arquitectura actual: son los componentes de
          producción (Spine, SessionDrawer, ChatHead, Composer, StatusStrip, LiveBar, los bloques
          del stream) con los mismos datos ({DRAWER.activeCount} abiertas · {DRAWER.savedCount}{" "}
          guardadas, seis estados) y una hoja de overrides con ámbito. Nada cambia de sitio.
          El estilo actual se ve en <a href="?view=desktop">Desktop</a> y{" "}
          <a href="?view=mobile">Phone</a>.
        </p>
      </header>
      <SkinRow skin={SKINS[0]} />
    </div>
  );
}
