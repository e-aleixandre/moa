import { useLayoutEffect, useRef, useState } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  StreamingSkeleton,
} from "../components/index.js";
import { Spinner } from "../primitives/index.js";
import { MobileTitleChip } from "../layout/mobile/MobileTitleChip/MobileTitleChip.jsx";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "../layout/mobile/MobileConversationScreen/MobileStream.css";
import "./responsive-lab.css";
import "./hydration-lab.css";

// HydrationLab — catalog-only exploration of the gap between TAPPING another
// session and its authoritative history arriving.
//
// The bug being designed for: on mobile only the OPEN session holds a
// WebSocket. Another session's attention arrives through the 15s roster poll
// (or push) with NO message content, so tapping it renders the transcript the
// client still had cached from the last visit — stale — and only swaps it when
// the WS `init` snapshot lands seconds later. Today there is no loading state
// on that path at all (MobileConversationScreen renders MobileStream
// unconditionally), and the unread dot is cleared on tap, acknowledging
// attention BEFORE showing what caused it.
//
// Every specimen mounts the REAL stream components (UserWaypoint,
// AssistantDocument, ActivityLedger, StreamingSkeleton) and the REAL
// MobileTitleChip inside the production `.mconv` / `.mconv-stream` chrome, so
// mobile density and tokens are the shipped ones. Only the fixtures, the
// 390px frame and the `.hydra-*` treatments are catalog-only; nothing here is
// imported by production app.js.

const noop = () => {};

// The session being opened lit up the roster while the user was elsewhere.
// Until `init` lands, the unread dot stays lit: attention is acknowledged by
// SHOWING its cause, never by the tap that goes looking for it.
const PENDING_ATTENTION = { unseen: 1, urgent: 0, arrival: 1 };
const RESOLVED_ATTENTION = {};

// ---- Fixtures ----------------------------------------------------------
// What the client still had cached from the last visit: the build finished,
// the agent said it would run the smoke check. Correct, just behind.

const CACHED_ROWS = [
  { id: "c1", tool: "read", arg: { text: "deploy/pulse-api.service" }, out: "38 lines", status: "ok" },
  {
    id: "c2",
    tool: "bash",
    arg: { text: "GOOS=linux go build -o /tmp/pulse-api ./cmd/api" },
    out: "exit 0",
    status: "ok",
  },
  {
    id: "c3",
    tool: "bash",
    arg: { text: "scp /tmp/pulse-api box:/usr/local/bin/pulse-api" },
    out: "12.4 MB",
    status: "ok",
  },
];

// What arrived while the session had no socket — the reason it asked for
// attention, and the part the stale view cannot know about.
const FRESH_ROWS = [
  {
    id: "f1",
    tool: "bash",
    arg: { text: "curl -s -o /dev/null -w '%{http_code}' box:8081/healthz" },
    out: "502",
    status: "err",
  },
  {
    id: "f2",
    tool: "bash",
    arg: { text: "journalctl --user -u pulse-api -n 40" },
    out: "40 lines",
    status: "ok",
  },
];

function CachedTranscript() {
  return (
    <>
      <UserWaypoint time="09:14">
        <p>
          Deploy the API to the tailnet box — build it and copy it over, but do
          not restart the service. Ask me first.
        </p>
      </UserWaypoint>

      <AssistantDocument>
        <div class="doc-prose">
          <p>
            Cross-compiling for the box and shipping the binary. The unit file
            already points at <code>/usr/local/bin/pulse-api</code>, so nothing
            else needs to move.
          </p>
        </div>
        <ActivityLedger rows={CACHED_ROWS} visibleDone={1} />
        <div class="doc-prose">
          <p>
            Binary is in place. Running the smoke check before I come back to
            you about the restart.
          </p>
        </div>
      </AssistantDocument>
    </>
  );
}

function FreshTail() {
  return (
    <AssistantDocument>
      <ActivityLedger rows={FRESH_ROWS} visibleDone={1} />
      <div class="doc-prose">
        <p>
          The smoke check answers <code>502</code>: the old process still holds{" "}
          <code>:8081</code>, so the new binary never bound. The build itself is
          fine.
        </p>
        <p>
          Releasing the port means restarting the unit, which is the line you
          asked me to stop at — may I run{" "}
          <code>systemctl --user restart pulse-api</code>?
        </p>
      </div>
    </AssistantDocument>
  );
}

// SkeletonTurns — variant A's stand-in for the whole transcript. It extends
// StreamingSkeleton rather than re-implementing it: the shimmer, its tempo and
// its prefers-reduced-motion opt-out all come from the shipped
// StreamingSkeleton.css, and only the bar geometry is restyled per shape.
function SkeletonTurns() {
  return (
    <div class="hydra-skel" aria-hidden="true">
      <StreamingSkeleton className="hydra-skel-quote" widths={["86%", "54%"]} />
      <StreamingSkeleton widths={["94%", "88%", "61%"]} />
      <StreamingSkeleton className="hydra-skel-card" widths={["100%", "100%"]} />
      <StreamingSkeleton widths={["90%", "72%", "43%"]} />
    </div>
  );
}

// Composer stand-in: the real MobileComposer is store-bound, so the frame gets
// a dumb bar for weight at the bottom of the screen (same trick the mobile
// gallery uses). Never focusable — the lab must not steal focus.
function ComposerBar() {
  return (
    <div class="hydra-composer" aria-hidden="true">
      <span class="hydra-composer-box">Message moa</span>
      <span class="hydra-composer-send">↑</span>
    </div>
  );
}

// Screen — one 390px phone surface: production `.mconv` column, the real title
// chip floating over it, and the stream in whichever hydration treatment the
// variant asks for.
function Screen({ variant, resolved }) {
  const waiting = !resolved;
  // The real MobileStream sticks to the bottom, so a top-anchored specimen
  // would misrepresent what the user is actually looking at when history
  // lands: the tail, not the opening message.
  const scrollerRef = useRef(null);
  useLayoutEffect(() => {
    const el = scrollerRef.current;
    if (!el) return;
    const pin = () => { el.scrollTop = Math.max(0, el.scrollHeight - el.clientHeight); };
    pin();
    // Web fonts and the ledger's own measurement land a frame later, so pin
    // again once layout has settled.
    const frame = requestAnimationFrame(pin);
    return () => cancelAnimationFrame(frame);
  }, [resolved, variant]);
  return (
    <div class={`mconv hydra-screen hydra--${variant}${waiting ? " is-waiting" : ""}`}>
      <div class="mstream">
        <div class="mconv-stream" ref={scrollerRef}>
          {variant === "banner" && waiting && (
            <div class="hydra-banner-spacer" aria-hidden="true" />
          )}

          {variant === "skeleton" && waiting ? (
            <SkeletonTurns />
          ) : (
            <div class="hydra-cached">
              <CachedTranscript />
            </div>
          )}

          {/* Variant D reserves the space the missing turn will occupy, right
              where it will land: at the tail, under a labelled hairline. */}
          {variant === "tail" && waiting && (
            <div class="hydra-tail">
              <div class="hydra-tail-rule">
                <span class="hydra-tail-label">
                  <Spinner size={9} color="blue" /> Catching up
                </span>
              </div>
              <StreamingSkeleton className="hydra-skel-card" widths={["100%"]} aria-hidden="true" />
              <StreamingSkeleton widths={["92%", "76%", "48%"]} aria-hidden="true" />
            </div>
          )}

          {resolved && <FreshTail />}
        </div>

        {/* A · a plain status line under the skeleton — the only words on an
            otherwise contentless screen. */}
        {variant === "skeleton" && waiting && (
          <p class="hydra-status" role="status">
            <Spinner size={11} color="blue" /> Loading conversation…
          </p>
        )}

        {/* B · the veil's own indicator, centred over the dimmed transcript. */}
        {variant === "veil" && waiting && (
          <div class="hydra-veil-note" role="status">
            <Spinner size={11} color="blue" /> Refreshing conversation…
          </div>
        )}

        {/* C · an unobtrusive chip under the title chip; the transcript below
            keeps its full contrast. */}
        {variant === "banner" && waiting && (
          <div class="hydra-banner" role="status">
            <Spinner size={10} color="blue" /> Catching up — this is your last view
          </div>
        )}

        {/* D · the same words as C, but the reserved tail already says where
            the gap is, so the chip only has to name it. */}
        {variant === "tail" && waiting && (
          <span class="sr-only" role="status">
            Catching up — showing your last view while the newest messages load
          </span>
        )}
      </div>

      <ComposerBar />

      <MobileTitleChip
        title="deploy pulse api"
        attention={waiting ? PENDING_ATTENTION : RESOLVED_ATTENTION}
        onToggle={noop}
      />
    </div>
  );
}

function Specimen({ variant, resolved, note }) {
  return (
    <article class="responsive-specimen hydra-specimen">
      <header class="responsive-specimen-head">
        <b>390px</b>
        <span>{note}</span>
      </header>
      <div class="hydra-frame">
        <Screen variant={variant} resolved={resolved} />
      </div>
    </article>
  );
}

const VARIANTS = [
  {
    id: "skeleton",
    label: "A · Full skeleton",
    note: "Trade-off: perfectly honest — nothing on screen can be mistaken for current — but the user pays for it with the loss of place, and a session they have visited fifty times greets them as a blank grey screen every time.",
  },
  {
    id: "veil",
    label: "B · Veiled stale content",
    note: "Trade-off: keeps the sense of place and cannot be misread as live, but it dims history that is actually correct — only incomplete — and dimmed code and diffs are genuinely hard to read on a phone in daylight.",
  },
  {
    id: "banner",
    label: "C · Stale + banner",
    note: "Trade-off: the cheapest to read and the easiest to ship, but the whole warning rests on one chip near the top, which the user scrolls away from in one flick and then has no signal at all.",
  },
  {
    id: "tail",
    label: "D · Trailing skeleton (recommended)",
    note: "Trade-off: puts the skeleton exactly where the missing messages will land, so history stays legible and the gap is visible from the bottom of the scroll, where the eye already is; it assumes history mostly APPENDS, so an in-place correction (a rewind, a tool row that finished) still swaps under the user without warning.",
  },
];

export function HydrationLab() {
  // `played` flips the waiting column into its resolved state so the arrival
  // itself — not just the two endpoints — can be judged. Re-keying the screens
  // replays the fade each time.
  const [played, setPlayed] = useState(0);
  const resolvedNow = played % 2 === 1;
  return (
    <section class="responsive-lab hydration-lab">
      <h2>Stale transcript hydration</h2>
      <p>
        Only the open session holds a WebSocket, so tapping a session that asked
        for attention shows the transcript the client cached on the last visit
        until the <code>init</code> snapshot lands. Four treatments for that gap,
        on the real stream components inside the production <code>.mconv</code>{" "}
        chrome. In every one the unread dot stays lit while the view is behind:
        attention is acknowledged by showing its cause, not by the tap that went
        looking for it.
      </p>
      <div class="hydra-controls">
        <button type="button" class="hydra-play" onClick={() => setPlayed((n) => n + 1)}>
          {resolvedNow ? "Replay gap" : "Play arrival"}
        </button>
        <span class="hydra-controls-hint">
          Flips the left column through the transition; the right column is the
          settled result.
        </span>
      </div>
      {VARIANTS.map((variant) => (
        <section class="responsive-lab-state" key={variant.id}>
          <h3>{variant.label}</h3>
          <p class="hydra-note">{variant.note}</p>
          <div class="responsive-specimens">
            <Specimen
              key={`${variant.id}-${played}`}
              variant={variant.id}
              resolved={resolvedNow}
              note={resolvedNow ? "arriving · history landed" : "waiting · no history yet"}
            />
            <Specimen variant={variant.id} resolved note="resolved · authoritative history" />
          </div>
        </section>
      ))}
      <section class="responsive-lab-state">
        <h3>Note on the recommendation</h3>
        <p class="hydra-note">
          D is C plus the one thing C is missing: a place. The cached turns are
          not wrong, so B's dimming charges the user for content that is fine,
          and A throws away a transcript it already has. The gap is at the tail —
          that is where the missing turns will usually render — so a skeleton
          there tells the truth, reserves a visible placeholder, and stays
          visible from the bottom of the stream, which is where the thumb already
          is. It cannot predict the exact height of an unknown turn; production
          preserves a scrolled-up reader's surviving block across the snapshot
          swap instead. Its honest weakness: it claims the gap is only at the
          end. When a message changes in place, the tail skeleton says nothing —
          which is why D keeps the "catching up" wording, and why the unread dot
          must survive until <code>init</code>.
        </p>
      </section>
    </section>
  );
}
