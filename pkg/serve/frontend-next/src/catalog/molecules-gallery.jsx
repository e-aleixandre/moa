import { useState } from "preact/hooks";
import {
  SessionRow,
  ModelPill,
  ModelSelector,
  PermissionCard,
  AskUserCard,
  FileCard,
  Card,
  Sheet,
  Toast,
  ToastTitle,
  ToastMessage,
} from "../components/index.js";
import { Button } from "../primitives/index.js";
import "./molecules-gallery.css";

const SESSION_VARIANTS = ["pill", "tab", "card"];

const SESSION_SAMPLES = [
  { title: "ws race fix", state: "running", active: true, meta: "running full suite · 0:41" },
  { title: "deploy pulse api", state: "permission", unseen: true, meta: "waiting for permission" },
  { title: "frontend polish", state: "idle", age: "2h", meta: "done · pushed 3 commits" },
  { title: "migrate sqlite", state: "error", unseen: true, meta: "provider 429 · retrying" },
  { title: "changelog 0.10", state: "saved", meta: "saved · no changes pending" },
];

const MODELS = [
  {
    id: "sol",
    name: "sol",
    provider: "openai",
    codename: "Sol",
    sub: "GPT-5.6 · 1M ctx",
    accent: "lavender",
  },
  {
    id: "fable",
    name: "fable",
    provider: "anthropic",
    codename: "Fable",
    sub: "5 · 1M ctx",
    accent: "peach",
  },
  {
    id: "terra",
    name: "terra",
    provider: "openai",
    codename: "Terra",
    sub: "GPT-5.6 · 1M ctx",
    accent: "teal",
  },
  {
    id: "haiku",
    name: "haiku",
    provider: "anthropic",
    codename: "Haiku",
    sub: "4.5 · 200K ctx",
    accent: "sky",
  },
];

function SessionRowVariant({ variant }) {
  return (
    <div class={`molecule-row session-row-board${variant === "tab" ? " tabline" : ""}`}>
      {SESSION_SAMPLES.map((s) => (
        <SessionRow key={s.title} variant={variant} onClose={() => {}} {...s} />
      ))}
    </div>
  );
}

function ModelPillRow() {
  return (
    <div class="molecule-row tight">
      <ModelPill model="sol" level="high" accent="lavender" />
      <ModelPill model="fable" level="xhigh" accent="peach" hot />
      <ModelPill model="terra" level="low" accent="teal" />
      <ModelPill model="haiku" level="off" accent="overlay1" />
    </div>
  );
}

function ModelSelectorDemo() {
  const [selected, setSelected] = useState("sol");
  const [thinking, setThinking] = useState("medium");
  const selectedModel = MODELS.find((m) => m.id === selected);
  return (
    <div class="molecule-row">
      <ModelSelector
        models={MODELS}
        selected={selected}
        thinking={thinking}
        onSelect={setSelected}
        onThinkingChange={setThinking}
      />
      <ModelPill
        model={selectedModel?.name ?? selected}
        level={thinking}
        accent={selectedModel?.accent ?? "lavender"}
      />
    </div>
  );
}

function FileCardDemo() {
  return (
    <div class="molecule-col">
      <FileCard file={{ name: "conversation-gallery.png", size: 521084, mime: "image/png", url: "/api/sessions/demo/files/demo-png" }} />
      <FileCard file={{ name: "COHERENCE-DECISIONS.md", size: 24310, mime: "text/markdown", url: "/api/sessions/demo/files/demo-md" }} />
      <FileCard file={{ name: "report.html", size: 88231, mime: "text/html", url: "/api/sessions/demo/files/demo-html" }} />
      <FileCard file={{ name: "redesign-backup.tar.gz", size: 355998, mime: "application/gzip", url: "/api/sessions/demo/files/demo-tgz" }} />
    </div>
  );
}

function PermissionCardDemo() {
  return (
    <div class="molecule-col">
      <PermissionCard
        title="moa wants to run"
        command="go test -race ./... && go vet ./..."
        scope={["cwd ~/dev/moa/main", "tool bash", "timeout 300s"]}
        alwaysLabel="go test"
        timer="waiting 0:07"
        onAllow={() => {}}
        onAlways={() => {}}
        onDeny={() => {}}
      />
      <PermissionCard
        variant="destructive"
        title="Destructive command — read carefully"
        command="git reset --hard origin/main && rm -rf ./dist"
        dangerTokens={["git reset --hard", "rm -rf"]}
        scope={[{ label: "deletes files", warn: true }, "cwd ~/dev/moa/main"]}
        timer="waiting 0:31"
        onAllow={() => {}}
        onDeny={() => {}}
      />
    </div>
  );
}

function AskUserCardDemo() {
  return (
    <div class="molecule-row">
      <AskUserCard
        question="The migration can run online (slower, zero downtime) or offline (fast, ~40s of downtime). Which do you prefer?"
        options={[
          { label: "Online — batched backfill, zero downtime", recommended: true },
          { label: "Offline — stop the service, migrate, restart" },
          { label: "Hold off — I'll decide later" },
        ]}
        onPick={() => {}}
        onSubmitFree={() => {}}
      />
    </div>
  );
}

function ToastDemo() {
  return (
    <div class="molecule-col toasts-demo">
      <Toast tone="success" onDismiss={() => {}}>
        <ToastTitle>frontend polish finished</ToastTitle>
        <ToastMessage>3 commits pushed · all checks green</ToastMessage>
      </Toast>
      <Toast
        tone="attention"
        onDismiss={() => {}}
        action={{ label: "Review →", onClick: () => {} }}
      >
        <ToastTitle>deploy pulse api needs you</ToastTitle>
        <ToastMessage>
          wants to run <b>systemctl --user restart…</b>
        </ToastMessage>
      </Toast>
      <Toast tone="error" onDismiss={() => {}}>
        <ToastTitle>migrate sqlite errored</ToastTitle>
        <ToastMessage>provider 429 · retrying (3/5)</ToastMessage>
      </Toast>
    </div>
  );
}

function CardDemo() {
  return (
    <div class="molecule-row">
      <Card style={{ maxWidth: "320px" }}>
        <p style={{ fontSize: "var(--text-sm)", color: "var(--subtext0)" }}>
          Card genérica — fondo mantle, borde surface0, radius lg. Base para
          paneles de contenido (esta galería la usa aquí mismo como ejemplo).
        </p>
      </Card>
    </div>
  );
}

function SheetDemo() {
  const [open, setOpen] = useState(false);
  return (
    <div class="molecule-row">
      <Button variant="solid" onClick={() => setOpen(true)}>
        Open sheet
      </Button>
      <Sheet open={open} onClose={() => setOpen(false)} title="New session">
        <p style={{ fontSize: "var(--text-sm)", color: "var(--subtext0)" }}>
          Estructura de panel modal + overlay, lista para alojar el picker de
          working directory (bloque siguiente). Cierra con Escape o clic
          fuera.
        </p>
      </Sheet>
    </div>
  );
}

// MoleculesGallery — shows the component system's molecules
// (Phase 1, block 2) in their variants/states, for visual review on /next.
export function MoleculesGallery() {
  return (
    <section>
      <h2>Moléculas</h2>

      <h3>SessionRow — variant "pill"</h3>
      <SessionRowVariant variant="pill" />

      <h3>SessionRow — variant "tab"</h3>
      <SessionRowVariant variant="tab" />

      <h3>SessionRow — variant "card"</h3>
      <SessionRowVariant variant="card" />

      <h3>ModelPill</h3>
      <ModelPillRow />

      <h3>ModelSelector</h3>
      <ModelSelectorDemo />

      <h3>PermissionCard</h3>
      <PermissionCardDemo />

      <h3>FileCard</h3>
      <FileCardDemo />

      <h3>AskUserCard</h3>
      <AskUserCardDemo />

      <h3>Card</h3>
      <CardDemo />

      <h3>Sheet</h3>
      <SheetDemo />

      <h3>Toast</h3>
      <ToastDemo />
    </section>
  );
}
