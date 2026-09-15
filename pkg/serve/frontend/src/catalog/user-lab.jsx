import { Rewind as RewindIcon } from "lucide-preact";
import { AssistantDocument } from "../components/AssistantDocument/AssistantDocument.jsx";
import { WaypointAttachments } from "../components/UserWaypoint/WaypointAttachments.jsx";
import "../components/UserWaypoint/UserWaypoint.css";
import "./user-lab.css";

// user-lab — what the user's message should look like.
//
// A MOCKUP for a decision, not the implementation: static markup wearing the
// production class names (UserWaypoint.css), so form, size, colour and type
// are the real ones, while nothing is wired (no rewind sheet, no lightbox).
// The three alternatives live here as lab classes (`.ul-a/b/c`) and a little
// extra markup, never as branches in the shipped UserWaypoint.
//
// Each row is the same short transcript twice — the desktop column at
// --content-block and the phone at 390 — because what is judged is not the
// message alone but how it alternates with the assistant's prose on the same
// axis. The cases are the ones that discriminate: the one-liner (where today
// the rewind mark floats 300px away from its own text), a long message with
// its own line breaks, attachments, a steer, and a task sent by the parent
// session.

// A 24px gradient PNG: enough for a real <img> thumbnail without a fixture.
const SHOT =
  "iVBORw0KGgoAAAANSUhEUgAAABgAAAAYCAIAAABvFaqvAAAELUlEQVR42g3Mscp2YACA4f+YlFLulFJKKaUUqadEkXpK1GexWCwWi8VisVgsFovFYnFO/3sdwPUvUohUIo1IJzKITCKLyCZyiFwij8gnCohCophIECVEGVFOVBJJooqoIfrjn1AQKkJD6AgDYSIshI1wEC7CQ/iIABEiYoRAJIgMkSNKhERUiAbxi1KFVCXVSHVSg9QktUhtUofUJfVIfdKANCSNSQVpQpqR5qQlqSStSBvSX1QoFCqFRqFTGBQmhUVhUzgULoVH4VMEFCFFTCEoEoqMIqcoKSRFRdFQ/CKpIFWkhtSRBtJEWkgb6SBdpIf0kQEyRMZIgUyQGTJHlkiJrJAN8hfVCrVKrVHr1Aa1SW1R29QOtUvtUfvUAXVIHVML6oQ6o86pS2pJXVE31L+oVWhVWo1WpzVoTVqL1qZ1aF1aj9anDWhD2phW0Ca0GW1OW9JK2oq2of1FnUKn0ml0Op1BZ9JZdDadQ+fSeXQ+XUAX0sV0gi6hy+hyupJO0lV0Dd0v6hV6lV6j1+kNepPeorfpHXqX3qP36QP6kD6mF/QJfUaf05f0kr6ib+h/0aAwqAwag85gMJgMFoPN4DC4DB6DzxAwhAwxg2BIGDKGnKFkkAwVQ8Pwi0aFUWXUGHVGg9FktBhtRofRZfQYfcaAMWSMGQVjwpgx5owlo2SsGBvGXzQpTCqTxqQzGUwmk8VkMzlMLpPH5DMFTCFTzCSYEqaMKWcqmSRTxdQw/aJZYVaZNWad2WA2mS1mm9lhdpk9Zp85YA6ZY2bBnDBnzDlzySyZK+aG+RctCovKorHoLAaLyWKx2CwOi8visfgsAUvIErMIloQlY8lZShbJUrE0LL9oVVhVVo1VZzVYTVaL1WZ1WF1Wj9VnDVhD1phVsCasGWvOWrJK1oq1Yf1Fm8KmsmlsOpvBZrJZbDabw+ayeWw+W8AWssVsgi1hy9hytpJNslVsDdsv2hV2lV1j19kNdpPdYrfZHXaX3WP32QP2kD1mF+wJe8aes5fskr1ib9h/0aFwqBwah85hcJgcFofN4XC4HB6HzxFwhBwxh+BIODKOnKPkkBwVR8Pxi06FU+XUOHVOg9PktDhtTofT5fQ4fc6AM+SMOQVnwplx5pwlp+SsOBvOX3QpXCqXxqVzGVwml8VlczlcLpfH5XMFXCFXzCW4Eq6MK+cquSRXxdVw/aJb4Va5NW6d2+A2uS1um9vhdrk9bp874A65Y27BnXBn3Dl3yS25K+6G+xc9Co/Ko/HoPAaPyWPx2DwOj8vj8fg8AU/IE/MInoQn48l5Sh7JU/E0PL/oVXhVXo1X5zV4TV6L1+Z1eF1ej9fnDXhD3phX8Ca8GW/OW/JK3oq34f1Fn8Kn8ml8Op/BZ/JZfDafw+fyeXw+X8AX8sV8gi/hy/hyvpJP8lV8Dd8f/wHwXZEf/9PskwAAAABJRU5ErkJggg==";

const ATTACHMENTS = [
  { type: "image", data: SHOT, attachment_size: 862208, mime_type: "image/png", filename: "hero-desktop.png" },
  { type: "document", attachment_id: "att-informe", attachment_size: 1468006, mime_type: "application/pdf", filename: "informe-q3.pdf" },
  { type: "document", attachment_id: "att-pricing", attachment_size: 6349, mime_type: "text/plain", filename: "pricing.tsx" },
];

export const VARIANTS = [
  {
    id: "actual",
    label: "Actual",
    note: "Referencia. Borde melocotón de 3px, sin tarjeta ni cabecera. El rewind vive en una fila propia bajo el texto (la de la hora, que hoy nunca se pinta) y se empuja al borde derecho del eje de 680px: con un mensaje corto queda a 300px del texto.",
  },
  {
    id: "a",
    label: "A · La cita",
    note: "El mensaje es lo que el asistente cita. Voz distinta, no caja: un paso más pequeño y más suave que la prosa, borde a 2px atenuado. El rewind es una marca al final del último renglón, a 8px del texto, siempre. Desaparece la fila vacía.",
  },
  {
    id: "b",
    label: "B · La marca de margen",
    note: "La gramática de EventBlock: el que interrumpe la prosa habla desde el margen. Un punto melocotón en el gutter a la altura de la primera línea, texto sangrado, y el rewind en la misma columna del gutter, debajo. Sin borde.",
  },
  {
    id: "c",
    label: "C · El bloque",
    note: "El mensaje es un objeto sobre el lienzo, como el ledger y el composer del que salió: misma superficie, mismo rim. Se ajusta al texto (un mensaje corto es un bloque corto) y el rewind va dentro, en su esquina. El melocotón deja de ser borde: queda libre.",
  },
];

function RewindMark() {
  return (
    <button type="button" class="wp-rewind" aria-label="Rewind the conversation to this message" title="Rewind here">
      <RewindIcon size={12} aria-hidden="true" />
    </button>
  );
}

// One user message. `paragraphs` is the text split on the user's own line
// breaks (pre-wrap keeps them in production; here they are already <p>s so
// the mockup does not depend on whitespace in JSX). Variant A puts the mark
// INSIDE the last paragraph, inline after the text; the others keep the
// production shape: body, then the `.zl-user-when` row.
function Msg({ variant, paragraphs, label, parent, attachments }) {
  const inline = variant === "a";
  const cls = `zl-user${parent ? " is-parent" : ""}${attachments ? " has-skirt" : ""}`;
  const last = paragraphs.length - 1;
  return (
    <div class={cls} style={parent ? { "--waypoint-accent": "var(--sky)" } : undefined}>
      {label && <div class="zl-user-label">{label}</div>}
      <div class="zl-user-body">
        {paragraphs.map((p, i) => (
          <p key={i}>
            {p}
            {inline && i === last && <RewindMark />}
          </p>
        ))}
      </div>
      {!inline && (
        <span class="zl-user-when zl-data">
          <RewindMark />
        </span>
      )}
      {attachments && <WaypointAttachments attachments={attachments} sessionId="demo" onOpenImage={() => {}} />}
    </div>
  );
}

function Transcript({ variant }) {
  return (
    <div class={`ul-col ul-${variant}`}>
      <Msg variant={variant} paragraphs={["Put fast on the desktop status strip, after yolo."]} />
      <AssistantDocument>
        <p>I'll hang it on the strip, same word as on the phone.</p>
      </AssistantDocument>
      <Msg
        variant={variant}
        paragraphs={[
          "Two things before you touch it:",
          <>1. The status strip already wraps at 1100px — check <code>StatusStrip.css</code> before adding a word.</>,
          "2. On the phone it goes in the capsule, not the strip. Same word, same colour.",
          "Then run the fidelity harness and tell me which scenes moved.",
        ]}
      />
      <AssistantDocument>
        <p>Done. The word sits after the permission chip, same colour as on mobile. Three scenes moved, all on the phone.</p>
      </AssistantDocument>
      <Msg variant={variant} paragraphs={["Mira esto y dime qué falla respecto al informe"]} attachments={ATTACHMENTS} />
      <Msg variant={variant} label="You — steer" paragraphs={["Skip the flaky e2e and rerun only the unit suite"]} />
      <Msg variant={variant} label="↳ FROM PARENT" parent paragraphs={["Audit pkg/serve/ws.go for the resume race and report the exact line."]} />
      <AssistantDocument>
        <p>Reading the reconnect path first to see how the snapshot and the subscription are sequenced.</p>
      </AssistantDocument>
    </div>
  );
}

function VariantRow({ variant }) {
  return (
    <section class="ul-variant" id={`user-${variant.id}`} data-variant={variant.id}>
      <header class="ul-variant-head">
        <h2>{variant.label}</h2>
        <p>{variant.note}</p>
      </header>
      <div class="ul-strip">
        <div class="ul-frame ul-desk" data-shot={`${variant.id}-desktop`}>
          <Transcript variant={variant.id} />
        </div>
        <div class="ul-frame ul-phone" data-shot={`${variant.id}-movil`}>
          <Transcript variant={variant.id} />
        </div>
      </div>
    </section>
  );
}

export function UserLab() {
  return (
    <div class="ul">
      <header class="ul-head">
        <h1>mensaje del usuario · <em>quién habla</em></h1>
        <p>
          Cuatro filas (actual + tres alternativas), la misma conversación en escritorio (680px) y en
          el móvil (390px). Lo que se juzga es la alternancia con la prosa del asistente y dónde queda
          el rewind respecto a su mensaje. Maqueta estática con las clases reales; nada está cableado.
        </p>
      </header>
      {VARIANTS.map((v) => <VariantRow key={v.id} variant={v} />)}
    </div>
  );
}
