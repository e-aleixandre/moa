import { useState } from "preact/hooks";
import {
  Laptop, MonitorSmartphone, QrCode, Smartphone, Tablet,
} from "lucide-preact";
import {
  ACTIVE, REVOKED, deviceKind, deviceLine, deviceState, expiringSoon,
  lifeFraction, sortDevices, untilLabel,
} from "../components/GlobalSettings/devices-model.js";
import "../components/GlobalSettings/GlobalSettings.css";
import "./devices-lab.css";

// devices-lab — CATALOG ONLY. Three directions for "which devices can reach
// this moa, and how do I throw one out", drawn INSIDE the real settings sheet.
//
// A MOCKUP for a decision, not the implementation: the sheet chrome is the
// shipped one (GlobalSettings.css, imported above, `.zl-set*` classes), so the
// head, the body padding, the section keys and the type are production's and
// cannot flatter the mockup. Only what a direction ADDS lives in
// devices-lab.css, under `.dl-a/b/c`, so the chosen one lifts across as-is.
//
// The data is the REAL contract and nothing more. /api/pulse/devices returns
// devicePublic (pkg/serve/device_auth.go): id, label, issued_at, expires_at,
// revoked_at?, last_used_at?. There is no model name, no IP, no OS version, no
// "this is you" flag — so none of the three draws one.
//
// The three are different answers to the same question, not three skins:
//
//   A · Roster   the settings sheet's own row grammar, extended. A device is a
//                row like every other setting; revoking is a second press on
//                the same control, which is the sheet's existing idiom.
//   B · Cards    a credential is an OBJECT with a life. Each one is a raised
//                card carrying the meter of its own 180 days, because the
//                thing being managed is a key that expires, not a preference.
//   C · Ledger   the data view: a state spine, mono readings, one row per
//                credential at the density of the activity ledger. Revoking
//                slides the row aside to uncover the destructive action.

const NOW = Date.parse("2026-09-10T12:00:00Z");
const DAY = 86400000;

const DEVICES = [
  {
    id: "b7Kq2mXpR4tLwN8vZcJ3Hf1s",
    label: "moa app (iPhone)",
    issued_at: new Date(NOW - 31 * DAY).toISOString(),
    expires_at: new Date(NOW + 149 * DAY).toISOString(),
    last_used_at: new Date(NOW - 4 * 60000).toISOString(),
  },
  {
    id: "Q9wE4rT6yU8iO0pA2sD5fG7h",
    label: "moa app (iPad)",
    issued_at: new Date(NOW - 171 * DAY).toISOString(),
    expires_at: new Date(NOW + 9 * DAY).toISOString(),
    last_used_at: new Date(NOW - 6 * 3600000).toISOString(),
  },
  {
    id: "Z1xC3vB5nM7kL9jH0gF2dS4a",
    label: "estudio (Linux)",
    issued_at: new Date(NOW - 12 * DAY).toISOString(),
    expires_at: new Date(NOW + 168 * DAY).toISOString(),
  },
  {
    id: "P6oI8uY0tR2eW4qA6sD8fG0h",
    label: "moa app (iPhone)",
    issued_at: new Date(NOW - 210 * DAY).toISOString(),
    expires_at: new Date(NOW - 30 * DAY).toISOString(),
    last_used_at: new Date(NOW - 45 * DAY).toISOString(),
    revoked_at: new Date(NOW - 40 * DAY).toISOString(),
  },
];

const GLYPH = { phone: Smartphone, tablet: Tablet, computer: Laptop, unknown: MonitorSmartphone };

function DeviceGlyph({ label, size = 15 }) {
  const Icon = GLYPH[deviceKind(label)];
  return <Icon size={size} strokeWidth={1.7} aria-hidden="true" />;
}

/* ── The sheet the three are drawn inside ────────────────────────────────
   Production's chrome, by class name: the head with back + title, the body,
   and the section key. Static here — the lab is judging the page's contents,
   and a working back button would only navigate a mockup. */
function SheetFrame({ phone, title, children }) {
  return (
    <div class={`dl-frame${phone ? " is-phone" : ""}`}>
      <div class={`zl-set${phone ? " is-phone" : ""} dl-set`}>
        <div class="zl-set-head is-sub">
          <span class="zl-set-back" aria-hidden="true">
            <svg viewBox="0 0 16 16" width="16" height="16">
              <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </span>
          <span class="zl-set-title">{title}</span>
          <span class="zl-x" aria-hidden="true">
            <svg viewBox="0 0 16 16"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
          </span>
        </div>
        <div class="zl-set-body is-sub">{children}</div>
      </div>
    </div>
  );
}

/* ── A · Roster ──────────────────────────────────────────────────────────
   The sheet already knows how to draw a list of things with a name, a line of
   what it is, and something on the right: that is every row in it. A device
   becomes one of those rows. Revoking is a second press on the same control —
   the idiom the dossier's Delete already uses (SessionPanel LifecycleActions),
   so there is nothing new to learn and nothing floats over the sheet. */
function RosterRow({ device, armed, onArm, onDisarm }) {
  const state = deviceState(device, NOW);
  const gone = state !== ACTIVE;
  const soon = expiringSoon(device, NOW);
  return (
    <div class={`dl-a-row${gone ? " is-gone" : ""}${armed ? " is-armed" : ""}`}>
      <span class={`dl-a-mark is-${deviceKind(device.label)}`} aria-hidden="true">
        <DeviceGlyph label={device.label} />
      </span>
      <span class="dl-a-txt">
        <span class="dl-a-n">{device.label}</span>
        <span class="dl-a-d">
          {deviceLine(device, NOW)}
          {!gone && <span class={`dl-a-left${soon ? " is-soon" : ""}`}> · {untilLabel(device.expires_at, NOW)} left</span>}
        </span>
      </span>
      {gone ? (
        <span class="dl-a-past">{state === REVOKED ? "Revoked" : "Expired"}</span>
      ) : armed ? (
        <span class="dl-a-confirm">
          <button type="button" class="dl-a-cancel" onClick={onDisarm}>Cancel</button>
          <button type="button" class="dl-a-go">Revoke</button>
        </span>
      ) : (
        <button type="button" class="dl-a-act" onClick={onArm} aria-label={`Revoke ${device.label}`}>
          Revoke
        </button>
      )}
    </div>
  );
}

function DirectionA({ phone }) {
  const [armed, setArmed] = useState(phone ? DEVICES[1].id : null);
  const list = sortDevices(DEVICES, NOW);
  return (
    <SheetFrame phone={phone} title="Devices">
      <div class="dl-a">
        <p class="zl-set-sum">
          Devices that can open this moa without the server's token. A credential
          lasts 180 days; revoking one ends it now, and that app returns to its
          pairing screen on its next request.
        </p>
        <div role="group" aria-label="Paired devices">
          {list.map((device) => (
            <RosterRow
              key={device.id}
              device={device}
              armed={armed === device.id}
              onArm={() => setArmed(device.id)}
              onDisarm={() => setArmed(null)}
            />
          ))}
        </div>
        <button type="button" class="dl-a-pair">
          <QrCode size={14} strokeWidth={1.8} aria-hidden="true" /> Pair a device…
        </button>
      </div>
    </SheetFrame>
  );
}

/* ── B · Cards ───────────────────────────────────────────────────────────
   What is being managed is not a preference: it is a KEY with a life, and the
   thing the owner cannot see today is how much of that life is left. So each
   credential is an object on the canvas — the shape the ledger and the
   composer already use — carrying its own meter. The action lives at the foot
   of the object it destroys, and the confirmation replaces that foot in place
   rather than opening anything. */
function CredentialCard({ device, armed, onArm, onDisarm }) {
  const state = deviceState(device, NOW);
  const gone = state !== ACTIVE;
  const soon = expiringSoon(device, NOW);
  const spent = lifeFraction(device, NOW);
  return (
    <article class={`dl-b-card${gone ? " is-gone" : ""}${armed ? " is-armed" : ""}`}>
      <header class="dl-b-head">
        <span class={`dl-b-mark${soon ? " is-soon" : ""}${gone ? " is-gone" : ""}`} aria-hidden="true">
          <DeviceGlyph label={device.label} size={17} />
        </span>
        <span class="dl-b-txt">
          <span class="dl-b-n">{device.label}</span>
          <span class="dl-b-d">{deviceLine(device, NOW)}</span>
        </span>
        <span class={`dl-b-pill is-${gone ? "gone" : soon ? "soon" : "ok"}`}>
          {gone ? (state === REVOKED ? "revoked" : "expired") : soon ? "expiring" : "active"}
        </span>
      </header>
      {!gone && (
        <div class="dl-b-life">
          <div class={`dl-b-meter${soon ? " is-soon" : ""}`}>
            <i style={`--spent:${spent}`} />
          </div>
          <span class={`dl-b-left${soon ? " is-soon" : ""}`}>{untilLabel(device.expires_at, NOW)} left</span>
        </div>
      )}
      <footer class="dl-b-foot">
        {gone ? (
          <span class="dl-b-note">No access. Pair again to restore it.</span>
        ) : armed ? (
          <>
            <span class="dl-b-ask">Ends access now.</span>
            <span class="dl-b-acts">
              <button type="button" class="dl-b-cancel" onClick={onDisarm}>Cancel</button>
              <button type="button" class="dl-b-go">Revoke</button>
            </span>
          </>
        ) : (
          <button type="button" class="dl-b-act" onClick={onArm}>Revoke access…</button>
        )}
      </footer>
    </article>
  );
}

function DirectionB({ phone }) {
  const [armed, setArmed] = useState(phone ? DEVICES[1].id : null);
  const list = sortDevices(DEVICES, NOW);
  return (
    <SheetFrame phone={phone} title="Devices">
      <div class="dl-b">
        <p class="zl-set-sum">
          Each paired app holds a credential of its own. Revoking one ends it
          immediately, everywhere it was open.
        </p>
        {list.map((device) => (
          <CredentialCard
            key={device.id}
            device={device}
            armed={armed === device.id}
            onArm={() => setArmed(device.id)}
            onDisarm={() => setArmed(null)}
          />
        ))}
        <button type="button" class="dl-b-pair">
          <QrCode size={14} strokeWidth={1.8} aria-hidden="true" /> Pair a device…
        </button>
      </div>
    </SheetFrame>
  );
}

/* ── C · Ledger ──────────────────────────────────────────────────────────
   The product's data view: a state spine down the left, the name in words and
   every reading in mono, at the ActivityLedger's density. Revoking slides the
   row aside to uncover the destructive action underneath it — the action is
   never on the surface, so it cannot be hit by a thumb passing through. The
   uncovered layer NAMES the device: the face it slid off is what was carrying
   the name, and a confirmation that does not say what it is about is a trap. */
function LedgerRow({ device, armed, onArm, onDisarm }) {
  const state = deviceState(device, NOW);
  const gone = state !== ACTIVE;
  const soon = expiringSoon(device, NOW);
  return (
    <div class={`dl-c-row${gone ? " is-gone" : ""}${armed ? " is-armed" : ""}`}>
      <div class="dl-c-under" aria-hidden={!armed}>
        <span class="dl-c-under-ask">Revoke <b>{device.label}</b>?</span>
        <button type="button" class="dl-c-cancel" onClick={onDisarm}>Cancel</button>
        <button type="button" class="dl-c-go">Revoke</button>
      </div>
      <div class="dl-c-face">
        <span class={`dl-c-spine is-${gone ? "gone" : soon ? "soon" : "ok"}`} aria-hidden="true" />
        <span class="dl-c-glyph" aria-hidden="true"><DeviceGlyph label={device.label} size={14} /></span>
        <span class="dl-c-txt">
          <span class="dl-c-n">{device.label}</span>
          <span class="dl-c-d">{deviceLine(device, NOW)}</span>
        </span>
        <span class={`dl-c-left${soon ? " is-soon" : ""}${gone ? " is-gone" : ""}`}>
          {gone ? "—" : untilLabel(device.expires_at, NOW).replace(" days", "d").replace(" day", "d")}
        </span>
        {!gone && (
          <button type="button" class="dl-c-act" onClick={onArm} aria-label={`Revoke ${device.label}`}>
            <svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true">
              <path d="M3 4.5h10M6.5 4.5V3.2h3v1.3M4.4 4.5l.6 8h6l.6-8" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </button>
        )}
      </div>
    </div>
  );
}

function DirectionC({ phone }) {
  const [armed, setArmed] = useState(phone ? DEVICES[1].id : null);
  const list = sortDevices(DEVICES, NOW);
  return (
    <SheetFrame phone={phone} title="Devices">
      <div class="dl-c">
        <p class="zl-set-sum">
          Credentials that can reach this moa. Revoking ends one immediately.
        </p>
        <div class="dl-c-tally">
          <span>{list.filter((d) => deviceState(d, NOW) === ACTIVE).length} active</span>
          <span class="dl-c-tally-sep" aria-hidden="true">·</span>
          <span>180-day credential</span>
        </div>
        <div role="group" aria-label="Paired devices">
          {list.map((device) => (
            <LedgerRow
              key={device.id}
              device={device}
              armed={armed === device.id}
              onArm={() => setArmed(device.id)}
              onDisarm={() => setArmed(null)}
            />
          ))}
        </div>
        <button type="button" class="dl-c-pair">
          <QrCode size={14} strokeWidth={1.8} aria-hidden="true" /> Pair a device…
        </button>
      </div>
    </SheetFrame>
  );
}

/* ── The empty state, per direction ──────────────────────────────────────
   Nothing is paired: the screen's whole job is then to offer the one thing
   that changes that. Drawn for each direction because an empty state written
   in a different voice from the list it replaces is how a screen ends up
   feeling like two screens. */
function EmptyA({ phone }) {
  return (
    <SheetFrame phone={phone} title="Devices">
      <div class="dl-a">
        <div class="dl-empty">
          <span class="dl-empty-mark" aria-hidden="true"><Smartphone size={20} strokeWidth={1.6} /></span>
          <p class="dl-empty-t">No device is paired</p>
          <p class="dl-empty-d">
            Pairing the moa app on a phone lets it reach this server without the
            token. You can end that access here at any time.
          </p>
          <button type="button" class="dl-empty-go">
            <QrCode size={14} strokeWidth={1.8} aria-hidden="true" /> Pair a device…
          </button>
        </div>
      </div>
    </SheetFrame>
  );
}

const DIRECTIONS = [
  {
    id: "a",
    label: "A · Roster",
    note: "The sheet's own row grammar, extended by one row type. A device is a row like every other setting: mark, name, one line of what it is, and its action on the right. Revoking arms in place — the row's right side becomes Cancel / Revoke, which is the dossier's existing two-press idiom. Nothing floats, nothing new to learn, and the section costs the sheet no new vocabulary.",
    Body: DirectionA,
  },
  {
    id: "b",
    label: "B · Credentials",
    note: "A credential is an object with a life, so it is drawn as one: a raised card on the canvas with the meter of its own 180 days, the same surface and rim the ledger uses. The meter is the thing the owner cannot see today — the iPad's nine remaining days are visible at a glance rather than in a date he has to subtract. Costs the most vertical room of the three.",
    Body: DirectionB,
  },
  {
    id: "c",
    label: "C · Ledger",
    note: "The data view. A state spine down the left, name in words, everything else mono, at the density of the activity ledger. Revoking slides the row aside to uncover the action, so the destructive control is never on the surface a thumb scrolls through. Four devices read at a glance; the trade is that the life of a credential is a number, not a picture.",
    Body: DirectionC,
  },
];

export function DevicesLab() {
  const params = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
  const only = params.get("dir");
  const empty = params.get("state") === "empty";
  const shown = only ? DIRECTIONS.filter((d) => d.id === only) : DIRECTIONS;
  return (
    <div class="zl dl">
      <div class="zl-aurora" aria-hidden="true" />
      <header class="dl-head">
        <h1>Paired <em>devices</em></h1>
        <p>
          Three directions for the same page inside Settings. The sheet chrome is
          production's (GlobalSettings.css); only what a direction adds is lab
          CSS. Every reading comes from a field /api/pulse/devices really
          returns — nothing on this page is invented data.
        </p>
      </header>
      {shown.map((direction) => (
        <section class={`dl-dir dl-${direction.id}-scope`} key={direction.id}>
          <div class="dl-dir-head">
            <h2>{direction.label}</h2>
            <p>{direction.note}</p>
          </div>
          <div class="dl-strip">
            <direction.Body />
            <direction.Body phone />
          </div>
        </section>
      ))}
      {(empty || !only) && (
        <section class="dl-dir dl-a-scope">
          <div class="dl-dir-head">
            <h2>Empty · nothing paired</h2>
            <p>
              The state the owner sees first. It is an invitation, not a hole:
              one sentence of what pairing buys him and the single action that
              does it.
            </p>
          </div>
          <div class="dl-strip">
            <EmptyA />
            <EmptyA phone />
          </div>
        </section>
      )}
    </div>
  );
}
