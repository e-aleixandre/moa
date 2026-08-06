import { useState } from "preact/hooks";
import { MobileTitleChip } from "../layout/mobile/MobileTitleChip/MobileTitleChip.jsx";
import { SessionDrawer } from "../layout/mobile/SessionDrawer/SessionDrawer.jsx";
import "./responsive-lab.css";
import "./title-attention-lab.css";

// TitleAttentionLab — catalog-only exploration of four treatments for the
// mobile unread-results indicator on the REAL MobileTitleChip. Every specimen
// mounts the shipped component (and, for the drawer fixture, the shipped
// SessionDrawer); the experimental styling lives in title-attention-lab.css
// behind `.title-attn-lab--*` wrapper classes that target the production
// markup, so nothing here touches production CSS or components.
//
// Ground rules mirrored from production: opening the drawer does NOT clear
// unread, there are never count badges (several unread must look identical to
// one), urgent wins over a new result, and all decoration stays aria-hidden.

const noop = () => {};

const WIDTHS = [
  { label: "320", width: 320, note: "narrow phone" },
  { label: "375", width: 375, note: "phone" },
];

const VARIANTS = [
  {
    id: "rail",
    label: "Rail",
    note: "A 2px mauve line along the lower edge of the title area replaces the detached dot.",
  },
  {
    id: "chevron",
    label: "Chevron",
    note: "The chevron well tints mauve — the door itself signals; the unread dot is hidden.",
  },
  {
    id: "wash",
    label: "Wash",
    note: "A faint state-coloured rim/gradient bleeds up from the chip's lower edge, paired with its matching dot.",
  },
  {
    id: "arrival",
    label: "Arrival",
    note: "The dot moves to the lower-right and announces itself with two finite ripples (~700ms × 2), then goes fully static next to a quiet chevron tint. Reduced motion gets a static ring instead.",
  },
  {
    id: "arrival-top-wash",
    label: "Arrival (top dot) + wash",
    note: "The state-coloured dot stays in its production top-right position and ripples twice on arrival, over a matching lower rim. Motion announces, the rim persists.",
  },
];

const FIXTURES = [
  { id: "quiet", label: "quiet", attn: {} },
  { id: "unread", label: "unread result", attn: { unseen: 1, urgent: 0 } },
  { id: "permission", label: "permission / ask", attn: { unseen: 0, urgent: 1 } },
  { id: "error", label: "error", attn: { unseen: 0, urgent: 1 } },
  { id: "mixed", label: "mixed — error wins", attn: { unseen: 2, urgent: 2 } },
];

// Attention while the drawer is open: production semantics — unread is NOT
// cleared by opening the list, so the treatment must coexist with .is-open.
const DRAWER_ATTN = { unseen: 2, urgent: 0 };

const DRAWER_NEW_RESULTS = [
  {
    id: "frontend",
    title: "frontend polish",
    state: "idle",
    when: "4m",
    last: "Done — pushed 3 commits, esbuild output rebuilt.",
    path: "~/dev/moa/frontend-polish",
    unseen: true,
  },
  {
    id: "sqlite",
    title: "migrate sqlite",
    state: "idle",
    when: "11m",
    last: "Migration verified against a copy of prod.",
    path: "~/dev/moa/migrate",
    unseen: true,
  },
];

const DRAWER_ACTIVE = [
  {
    id: "ws",
    title: "ws race fix",
    state: "running",
    when: "now",
    last: "Running the full suite after the resume() fix…",
    path: "~/dev/moa/main",
    active: true,
  },
];

// TranscriptFiller — muted text-shaped bars behind the chip, so its blur pill
// reads over "content" the way it does over a real conversation.
function TranscriptFiller() {
  return (
    <div class="talab-transcript" aria-hidden="true">
      <span /><span /><span /><span />
    </div>
  );
}

function ChipSpecimen({ variant, fixture, width, replay }) {
  return (
    <article class="responsive-specimen talab-specimen" style={{ width: `${width.width}px` }}>
      <header class="responsive-specimen-head">
        <b>{width.label}px</b><span>{fixture.label}</span>
      </header>
      <div class={`talab-frame talab-frame--${fixture.id} title-attn-lab--${variant.id}`}>
        <TranscriptFiller />
        <MobileTitleChip
          key={variant.id.startsWith("arrival") ? replay : undefined}
          title="ws race fix"
          attention={fixture.attn}
          onToggle={noop}
        />
      </div>
    </article>
  );
}

// DrawerSpecimen — the chip in its open state with the REAL SessionDrawer
// unfurled beneath it, two "New results" entries showing. The unread signal
// stays on the chip: opening the drawer does not clear it.
function DrawerSpecimen({ variant, width, replay }) {
  return (
    <article class="responsive-specimen talab-specimen" style={{ width: `${width.width}px` }}>
      <header class="responsive-specimen-head">
        <b>{width.label}px</b><span>drawer open · unread kept</span>
      </header>
      <div class={`talab-frame talab-frame--drawer title-attn-lab--${variant.id}`}>
        <TranscriptFiller />
        <MobileTitleChip
          key={variant.id.startsWith("arrival") ? replay : undefined}
          title="ws race fix"
          attention={DRAWER_ATTN}
          open
          onToggle={noop}
        />
        <SessionDrawer
          open
          onClose={noop}
          newResults={DRAWER_NEW_RESULTS}
          active={DRAWER_ACTIVE}
          saved={[]}
          activeCount={3}
          savedCount={1}
          projects={[]}
          onSelect={noop}
          onCreate={noop}
          onSettings={noop}
          onCloseSession={noop}
          onReopenSession={noop}
          onDeleteSession={noop}
        />
      </div>
    </article>
  );
}

// KeyboardSpecimen — a short 375px frame approximating an open soft keyboard:
// a focused composer textarea sits at the bottom above a key slab, so the eye
// starts at the bottom and the treatment has to pull it up to the title.
function KeyboardSpecimen({ variant, replay }) {
  return (
    <article class="responsive-specimen talab-specimen" style={{ width: "375px" }}>
      <header class="responsive-specimen-head">
        <b>375px</b><span>keyboard open · one unread</span>
      </header>
      <div class={`talab-frame talab-frame--keyboard title-attn-lab--${variant.id}`}>
        <TranscriptFiller />
        <MobileTitleChip
          key={variant.id.startsWith("arrival") ? replay : undefined}
          title="ws race fix"
          attention={{ unseen: 1, urgent: 0 }}
          onToggle={noop}
        />
        <div class="talab-composer">
          <textarea
            class="talab-composer-input"
            rows={1}
            readOnly
            value="and once the tests pass,"
            aria-label="Message moa (specimen)"
          />
          <button type="button" class="talab-composer-send" aria-label="Send" tabIndex={-1}>
            ↑
          </button>
        </div>
        <div class="talab-keyboard" aria-hidden="true">
          {[10, 9, 7].map((count, row) => (
            <div class="talab-key-row" key={row}>
              {Array.from({ length: count }, (_, i) => <span class="talab-key" key={i} />)}
            </div>
          ))}
          <div class="talab-key-row">
            <span class="talab-key talab-key--space" />
          </div>
        </div>
      </div>
    </article>
  );
}

export function TitleAttentionLab() {
  // Bumping `replay` re-keys the arrival chips so they remount and the finite
  // ripple animation plays again from the start.
  const [replay, setReplay] = useState(0);
  return (
    <section class="responsive-lab title-attn-lab">
      <h2>Mobile title attention</h2>
      <p>
        Four alternative treatments for the cross-session unread indicator on the
        real MobileTitleChip. Styling is scoped to catalog-only wrapper classes
        over the production markup — several unread looks identical to one (no
        counts), urgent keeps the untouched production peach, and opening the
        drawer never clears unread.
      </p>
      {VARIANTS.map((variant) => (
        <section class="responsive-lab-state" key={variant.id}>
          <div class="talab-state-head">
            <h3>{variant.label}</h3>
            {variant.id.startsWith("arrival") && (
              <button type="button" class="talab-replay" onClick={() => setReplay((n) => n + 1)}>
                Replay arrival
              </button>
            )}
          </div>
          <p class="talab-note">{variant.note}</p>
          <div class="responsive-specimens">
            {FIXTURES.map((fixture) =>
              WIDTHS.map((width) => (
                <ChipSpecimen
                  key={`${fixture.id}-${width.label}`}
                  variant={variant}
                  fixture={fixture}
                  width={width}
                  replay={replay}
                />
              ))
            )}
            {WIDTHS.map((width) => (
              <DrawerSpecimen key={`drawer-${width.label}`} variant={variant} width={width} replay={replay} />
            ))}
            <KeyboardSpecimen variant={variant} replay={replay} />
          </div>
        </section>
      ))}
    </section>
  );
}
