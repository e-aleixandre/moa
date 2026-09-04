import { useState } from "preact/hooks";
import {
  SessionRow,
  ModelPill,
  ModelSelector,
  PermissionCard,
  AskUserCard,
  FileCard,
  UserWaypoint,
  Card,
  Sheet,
  Toast,
  ToastTitle,
  ToastMessage,
} from "../components/index.js";
import { Button } from "../primitives/index.js";
import { parsePreviewReference, feedbackMessage } from "../data/util/preview-reference.js";
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
    catalogId: "sol",
    name: "sol",
    provider: "openai",
    codename: "Sol",
    sub: "GPT-5.6 · 1M ctx",
    accent: "lavender",
  },
  {
    id: "fable",
    catalogId: "fable",
    name: "fable",
    provider: "anthropic",
    codename: "Fable",
    sub: "5 · 1M ctx",
    accent: "peach",
  },
  {
    id: "terra",
    catalogId: "terra",
    name: "terra",
    provider: "openai",
    codename: "Terra",
    sub: "GPT-5.6 · 1M ctx",
    accent: "teal",
  },
  {
    id: "haiku",
    catalogId: "haiku",
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


// Two tiny gradient PNGs: enough for a real <img> thumbnail (the row's own
// image path, not a fake block) without shipping fixture files.
const SHOT_A =
  "iVBORw0KGgoAAAANSUhEUgAAABgAAAAYCAIAAABvFaqvAAADB0lEQVR42l3Q6VLbWBSFUT1JY2u0LenOeNLsAWMwkJAmnfd/kpyzrxTSVO3fq77awSo/0/L8IS8uRfFYlNeyfBI08SzFTcoXJV+1eqMZ9c3o71a/W/PDmX/vafZjbX+u3X8b9ytY5idYBMEqH0tviWe2JCzlLYJgmXfnLfvBlmOLoONoAcrLyRKwGLopNVmAjJksC4uhn8GCIVgFLEBsCVgSFkOwNCxAbFlYji2CDrBOS4bOq3KyAJWSLam89aoYetNmsgA5x1aQ5cNoFbAYgiVgSbKehZosDYshWBaWIwtQxtBhUcAqx6hcwJJsESQYukkNy4xRxsJybAVp3o9WcWSrPK28JcgiCJaaLP3ClnnV3rJkEcQWQ6NVwCp91HklYE3QaGlYxke9aQvLQ0nRpQVZBMEqOWoJiC0JS3mLIFiGoxQgthxbQSLbRHSp6NOSoaycLAGLoUuhJguQMJNlYTH0PYhNm+g2UV0q+1QMHmJLwJKwGIKlYQFiy8JybAXxfRO7NrFdYvpU96kaMnlYiOMC0FKylStvXQuGnkozWYCUYyuItnW8aeJ1m7gusX1qhkwPmTosJCxJ1sNKTZaGxRAsC8uRRVBVR/sm3jbxpk3WXeL61A6ZOWQalmSLoBVDl1zDMmOUsLAcW0HYVlFdR1UT79t42yabLr3vUzdklqzjQsFSk6Uf2TLX0luWLILYCsKhCrsqauuobuKqjXddsu3T9d8WQ6OlYRkf9VRaWB6an/bhoQr7OurqqGEr2XtrSO+/WATBMhxVAGLLsRXML7v5eR8eq3Agq4naJq4na0PWgazFHwvQykyWhcXQLZhdd/PH3fxhH56q8FBHfRO37ae19tZxtDQsQGxZWI6tYHbbzp7I2s8fqv9bXbLvvZW5Q2YYWjB0XprJAlQ4toK7t83sZTt73s2v+/mlCs919NUa2PJnaVgMwbKwHFkEvW/uvm1nr1+s5tPafZ41WmaMWllYjq3gn4/13Q9v7WY3tkKyTt5q46aNK1h0lmOILXNaesuSRRBbvwG4AnKJ8CCrxgAAAABJRU5ErkJggg==";
const SHOT_B =
  "iVBORw0KGgoAAAANSUhEUgAAABgAAAAYCAIAAABvFaqvAAAFSklEQVR42gXBCwpEUAAAQGeyifQQiXwjEaWklFKkRIiUlJSUlJSUlFxzZyAAJySc0HDCwgkPJyKcKHCiwYkBJxacOHDiwokHJz6cBHASwkkEJzGcpHCSwUkBJxWcNHDSwQkEfin5S+lfyv5S/peKv1T5pdovNX6p9UudX+r+Uu+X+r80+KXhL41+afxL01+a/dLil1a/tPml3S+FAJKRSEYjGYtkPJKJSKYgmYZkBpJZSOYgmYtkHpL5SBYgWYhkEZLFSJYiWYZkBZJVSNYgWYdkEEBzEs1pNGfRnEdzEc0VNNfQ3EBzC80dNHfR3ENzH80DNA/RPELzGM1TNM/QvEDzCs0bNO/QHAJYSWIljZUsVvJYKWKlgpUaVhpYaWGlg5UuVnpY6WNlgJUhVkZYGWNlipUZVhZYWWFlg5UdVkIAr0m8pvGaxWser0W8VvBaw2sDry28dvDaxWsPr328DvA6xOsIr2O8TvE6w+sCryu8bvC6w2sIgJYELQ1aFrQ8aEXQKqDVQGuA1gKtA1oXtB5ofdAGoA1BG4E2Bm0K2gy0BWgr0Dag7UALAaIjiY4mOpboeKITiU4hOo3oDKKziM4hOpfoPKLziS4gupDoIqKLiS4luozoCqKriK4huo7oIED1JNXTVM9SPU/1ItUrVK9RvUH1FtU7VO9SvUf1PtUHVB9SfUT1MdWnVJ9RfUH1FdU3VN9RPQTogaQHmh5YeuDpQaQHhR40ejDowaIHhx5cevDowaeHgB5CeojoIaaHlB4yeijooaKHhh46eoAAM5LMSDMjy4w8M4rMqDCjxowGM1rM6DCjy4weM/rMGDBjyIwRM8bMmDJjxowFM1bM2DBjx4wQYCeSnWh2YtmJZyeRnRR20tjJYCeLnRx2ctnJYyefnQJ2CtkpYqeYnVJ2ytipYKeKnRp26tgJAtxMcjPNzSw389wscrPCzRo3G9xscbPDzS43e9zsc3PAzSE3R9wcc3PKzRk3F9xccXPDzR03Q4BfSH6h+YXlF55fRH5R+EXjF4NfLH5x+MXlF49ffH4J+CXkl4hfYn5J+SXjl4JfKn5p+KXjFwgIKymstLCywsoLqyisirBqwmoIqyWsjrC6wuoJqy+sgbCGwhoJayysqbBmwloIayWsjbB2wgoBaSOljZY2Vtp4aROlTZE2TdoMabOkzZE2V9o8afOlLZC2UNoiaYulLZW2TNoKaaukrZG2TtogIO+kvNPyzso7L++ivCvyrsm7Ie+WvDvy7sq7J+++vAfyHsp7JO+xvKfynsl7Ie+VvDfy3sk7BNSDVA9aPVj14NVDVA9FPTT1MNTDUg9HPVz18NTDV49APUL1iNQjVo9UPTL1KNSjUo9GPTr1gIB2ktpJayernbx2itqpaKemnYZ2WtrpaKernZ52+toZaGeonZF2xtqZamemnYV2VtrZaGennRDQL1K/aP1i9YvXL1G/FP3S9MvQL0u/HP1y9cvTL1+/Av0K9SvSr1i/Uv3K9KvQr0q/Gv3q9AsCxk0aN23crHHzxi0at2LcmnEbxm0Zt2PcrnF7xu0bd2DcoXFHxh0bd2rcmXEXxl0Zd2PcnXFDwHxI86HNhzUf3nxE81HMRzMfw3ws83HMxzUfz3x88wnMJzSfyHxi80nNJzOfwnwq82nMpzMfCFgvab209bLWy1uvaL2K9WrWa1ivZb2O9brW61mvb72B9YbWG1lvbL2p9WbWW1hvZb2N9XbWCwH7I+2Ptj/W/nj7E+1PsT/N/gz7s+zPsT/X/jz78+0vsL/Q/iL7i+0vtb/M/gr7q+yvsb/O/v50i6QPzK/3+gAAAABJRU5ErkJggg==";

function attachment(filename, mime, size, type = "document") {
  return { type, attachment_id: `att-${filename}`, attachment_size: size, mime_type: mime, filename };
}

function image(filename, size, shot = SHOT_A) {
  return { type: "image", data: shot, attachment_size: size, mime_type: "image/png", filename };
}

const SKIRT_SAMPLES = [
  ["1 attachment", [image("hero-desktop.png", 862208)]],
  [
    "3 attachments",
    [
      image("hero-desktop.png", 862208),
      attachment("informe-q3.pdf", "application/pdf", 1468006),
      attachment("pricing.tsx", "text/plain", 6349),
    ],
  ],
  [
    "8 attachments",
    [
      image("hero-desktop.png", 862208),
      image("hero-mobile.png", 317440, SHOT_B),
      attachment("informe-q3.pdf", "application/pdf", 1468006),
      attachment("pricing.tsx", "text/plain", 6349),
      attachment("Pricing.test.tsx", "text/plain", 3174),
      attachment("logo.svg", "image/svg+xml", 4096, "image"),
      attachment("notes.md", "text/markdown", 1229),
      attachment("brief.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", 225280),
    ],
  ],
  [
    "not available on this device",
    [attachment("informe.xls", "application/vnd.ms-excel", 0)].map(({ filename, mime_type, type }) => ({
      type,
      filename,
      mime_type,
    })),
  ],
];

function WaypointSkirtDemo() {
  return (
    <div class="molecule-col">
      {SKIRT_SAMPLES.map(([label, attachments]) => (
        <div key={label}>
          <p class="molecule-case">{label}</p>
          <UserWaypoint time="9:20" attachments={attachments} sessionId="demo" onRewind={() => {}}>
            <p>Mira esto y dime qué falla respecto al informe</p>
          </UserWaypoint>
        </div>
      ))}
    </div>
  );
}

const REFERENCE_SAMPLES = [
  [
    "with visible text",
    "Este botón es demasiado grande",
    {
      tag: "button",
      id: "buy",
      classes: ["btn", "btn-primary"],
      text: "Comprar",
      path: "/pricing",
      url: "http://host:5173/pricing",
      ancestors: ["section#pricing", "div.card.card--pro", "div.card__footer"],
      selector: "#pricing .card--pro button#buy",
      attrs: { "data-plan": "pro", "aria-label": "Comprar el plan Pro" },
    },
  ],
  [
    "no visible text (tag + accessible name)",
    "Esta imagen se ve borrosa en retina, usa el @2x que hay en assets/",
    {
      tag: "img",
      classes: ["hero-shot"],
      path: "/",
      url: "http://host/",
      ancestors: ["main", "section.hero", "figure"],
      attrs: { alt: "Captura del dashboard" },
    },
  ],
  [
    "no text and no distinctive container",
    "Aquí el espaciado se rompe",
    {
      tag: "div",
      id: "overlay",
      classes: ["backdrop"],
      path: "/about",
      url: "http://host/about",
      ancestors: ["div.container", "div.wrap"],
    },
  ],
  [
    "no comment — only the reference",
    "",
    {
      tag: "input",
      classes: ["field"],
      path: "/signup",
      url: "http://host/signup",
      ancestors: ["form.signup", "div.field-group"],
      attrs: { placeholder: "Tu correo" },
    },
  ],
];

function PreviewReferenceDemo() {
  return (
    <div class="molecule-col">
      {REFERENCE_SAMPLES.map(([label, comment, element]) => {
        const parsed = parsePreviewReference(feedbackMessage(comment, element));
        return (
          <div key={label}>
            <p class="molecule-case">{label}</p>
            <UserWaypoint time="9:38" reference={parsed.reference} onRewind={() => {}}>
              {parsed.comment && <p>{parsed.comment}</p>}
            </UserWaypoint>
          </div>
        );
      })}
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
// in their variants/states, for visual review on /next.
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

      <h3>UserWaypoint — preview reference (anclado al lomo)</h3>
      <PreviewReferenceDemo />

      <h3>UserWaypoint — attachments skirt (faldón)</h3>
      <WaypointSkirtDemo />

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
