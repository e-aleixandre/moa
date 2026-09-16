// fidelity-freeze first, deliberately: it patches Date.now and Math.random,
// and the fixtures read the clock at module scope (specimen.js:10,
// catalog-backend.js:48). An import placed lower would freeze the clock after
// the ages had already been computed from the real one. Inert unless
// ?view=scene.
import "./catalog/fidelity-freeze.js";
import { render } from "preact";
import { useEffect } from "preact/hooks";
import "./index.css";
import { ConversationScreen, PaneGridScreen, MobileConversationScreen, DesktopShell } from "./layout/index.js";
import { ToastContainer, CommandPalette } from "./components/index.js";
import { store } from "./data/store.js";
import { useStore } from "./hooks/useStore.js";
import { closePalette } from "./data/palette.js";
import { setMobile } from "./data/tile-actions.js";
import { Catalog } from "./catalog/catalog.jsx";
import { LiveStatesGallery } from "./catalog/live-states-gallery.jsx";
import { MobileGallery } from "./catalog/mobile-gallery.jsx";
import { SubagentGallery } from "./catalog/subagent-gallery.jsx";
import { DesktopLab, PhoneLab } from "./catalog/desktop-lab.jsx";
import { ZonesLab, ZonesPhone, LIVE_STATES } from "./catalog/zones-lab.jsx";
import { Scene } from "./catalog/scene.jsx";
import { InboxLab } from "./catalog/zones-inbox.jsx";
import { WorkLab } from "./catalog/zones-work.jsx";
import { UserLab } from "./catalog/user-lab.jsx";
import { U2Lab } from "./catalog/u2-lab.jsx";
import { U6Lab } from "./catalog/u6-lab.jsx";
import { LPLab } from "./catalog/lp-lab.jsx";
import { LP2Lab } from "./catalog/lp2-lab.jsx";
import { seedCatalogStore } from "./catalog/specimen.js";
import { installCatalogBackend } from "./catalog/catalog-backend.js";
import { announceArrivals } from "./data/events.js"; // wake-on-event

// catalog-app — design lab. Not shipped in the production binary. Served by
// `npm run catalog`: the real screens in frames, plus the token/live galleries.
// A change to ChatHead is a change here because there is no second chrome.
// The Go server is replaced by catalog-backend (fixtures + one init frame).

installCatalogBackend({ getSessions: () => store.get().sessions });
seedCatalogStore();

const LINKS = [
  { key: "desktop", label: "Desktop", href: "?view=desktop" },
  { key: "mobile", label: "Phone", href: "?view=mobile" },
  { key: "grid", label: "Grid", href: "?view=grid" },
  { key: "catalog", label: "Tokens", href: "?view=catalog" },
  { key: "live", label: "Live", href: "?view=live" },
  { key: "subagent", label: "Subagent", href: "?view=subagent" },
  { key: "pieces", label: "Mobile pieces", href: "?view=pieces" },
  { key: "zones", label: "Zones", href: "?view=zones" },
  { key: "phone", label: "Phone", href: "?view=phone" },
  { key: "inbox", label: "Inbox", href: "?view=inbox" },
  { key: "work", label: "Work", href: "?view=work" },
  { key: "user", label: "User", href: "?view=user" },
  { key: "u2", label: "User 2", href: "?view=u2" },
  { key: "u6", label: "User 6", href: "?view=u6" },
  { key: "lp", label: "Live Preview", href: "?view=lp" },
  { key: "lp2", label: "Live Preview 2", href: "?view=lp2" },
];

// wake-on-event: the two inbox seed sets are a URL away from each other, so
// the same real screen can be judged with one thing waiting and with a night's
// worth of noise. The link keeps whichever view is open.
function eventsToggleHref(view, noisy) {
  const params = new URLSearchParams();
  params.set("view", view);
  if (!noisy) params.set("events", "noisy");
  return `?${params.toString()}`;
}

function Nav({ current }) {
  const noisy = typeof location !== "undefined" && new URLSearchParams(location.search).get("events") === "noisy";
  return (
    <nav class="catalog-nav" aria-label="Design lab">
      {LINKS.map((v) => (
        <a
          key={v.key}
          href={noisy ? `${v.href}&events=noisy` : v.href}
          aria-current={v.key === current ? "page" : undefined}
          style={{ color: v.key === current ? "var(--peach)" : "var(--lavender)" }}
        >
          {v.label}
        </a>
      ))}
      {/* wake-on-event */}
      <a href={eventsToggleHref(current, noisy)} style={{ color: noisy ? "var(--peach)" : "var(--lavender)" }}>
        {noisy ? "Events: noisy" : "Events: 1 pending"}
      </a>
    </nav>
  );
}

function useCatalogBootstrap() {
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 768px)");
    const handler = (e) => setMobile(e.matches);
    handler(mq);
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, []);
  // wake-on-event: show the arrival toast once on load, through the SHIPPED
  // path (announceArrivals → notifications → ToastContainer), so what the owner
  // sees in the lab is what a real hook produces. The event chosen is one that
  // is NOT on screen — the rule is that the visible conversation never toasts.
  useEffect(() => {
    const t = setTimeout(() => {
      const pending = store.get().events.find((e) => (e.state || "new") === "new");
      if (pending) announceArrivals([pending], { visible: [] });
    }, 700);
    return () => clearTimeout(t);
  }, []);
}

function LabPalette() {
  const open = useStore((s) => s.paletteOpen);
  const step = useStore((s) => s.paletteStep);
  return (
    <CommandPalette
      open={open}
      onClose={closePalette}
      context="conversation"
      focusedPane={null}
      initialStep={step}
    />
  );
}

/* The phone alone, filling the window.

   ?view=zones draws every density on one scrollable page, which is the right
   shape for COMPARING them and the wrong one for LOOKING at the phone: you
   open it and have to go find it. This route opens straight into the phone.

   The prototype's phone is a 390x780 box with a radius and a drop shadow --
   it is drawn as an object sitting on a table. Here it is the screen itself,
   so the frame comes off (no radius, no shadow, no fixed size) and the phone
   fills whatever window it is given. Nothing else changes: same component,
   same markup, same CSS as the zones page. */
function PhoneAlone() {
  // `live` is required, not optional: useLive reads preset.open on the first
  // render (zones-lab.jsx:1026), so an absent preset throws before anything
  // paints. "working" is the zones page's own default (LIVE_STATES[1]).
  const params = new URLSearchParams(location.search);
  const live = LIVE_STATES.find((s) => s.id === params.get("live")) || LIVE_STATES[1];

  // The phone keeps its 390x780 and is scaled to fit. Resizing it instead --
  // which is what "fullscreen" suggests -- changes every proportion, because
  // the type does not grow with the box. The factor is min(vw/390, vh/780),
  // a ratio of two lengths, which CSS has no way to express.
  useEffect(() => {
    const fit = () => {
      const k = Math.min(innerWidth / 390, innerHeight / 780);
      document.documentElement.style.setProperty("--cat-phone-scale", String(k));
    };
    fit();
    addEventListener("resize", fit);
    return () => removeEventListener("resize", fit);
  }, []);

  return (
    // `.zl` is the prototype's own stage: its thirty tokens and its aurora are
    // defined there, so the phone has to be INSIDE it to look like itself.
    <div class="zl is-phone-alone">
      <div class="zl-aurora" aria-hidden="true" />
      <ZonesPhone label="" live={live} surface={params.get("surface") || "none"} />
    </div>
  );
}

function CatalogApp() {
  useCatalogBootstrap();
  const view = useStore((s) => s.view || "desktop");

  useEffect(() => {
    document.documentElement.classList.remove("mobile-locked");
  }, [view]);

  let body = null;
  if (view === "catalog") body = <Catalog />;
  else if (view === "live") body = <LiveStatesGallery />;
  else if (view === "subagent") body = <SubagentGallery />;
  else if (view === "pieces") body = <MobileGallery />;
  else if (view === "zones") body = <ZonesLab />;
  // ?view=phone is the prototype's phone ALONE, filling the window. ?view=zones
  // shows every density at once on a scrollable page, which is right for
  // comparing them and wrong for looking at one: the owner asked to open a URL
  // and land in the phone, not to scroll to it. Same component as the zones
  // page -- the frame is what the wrapper drops, not the screen.
  else if (view === "phone") body = <PhoneAlone />;
  else if (view === "inbox") body = <InboxLab />;
  else if (view === "work") body = <WorkLab />;
  else if (view === "user") body = <UserLab />;
  else if (view === "u2") body = <U2Lab />;
  else if (view === "u6") body = <U6Lab />;
  else if (view === "lp") body = <LPLab />;
  else if (view === "lp2") body = <LP2Lab />;
  else if (view === "mobile") {
    body = (
      <PhoneLab>
        <MobileConversationScreen forceMobile />
      </PhoneLab>
    );
  } else if (view === "grid") {
    body = (
      <DesktopLab>
        <DesktopShell>
          <PaneGridScreen />
        </DesktopShell>
      </DesktopLab>
    );
  } else {
    body = (
      <DesktopLab>
        <DesktopShell>
          <ConversationScreen />
        </DesktopShell>
      </DesktopLab>
    );
  }

  return (
    <>
      {body}
      <Nav current={view} />
      <LabPalette />
      <ToastContainer />
    </>
  );
}

// ?view=scene is not a view of the catalogue app: it is ONE scene with nothing
// around it, for the fidelity harness. Short-circuited here rather than added
// to the chain above because everything CatalogApp mounts around `body` would
// land in the capture — the nav, the palette, and the arrival toast that
// useCatalogBootstrap fires 700ms after load, which is both chrome and a race.
const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
if (params.get("view") === "scene") {
  render(<Scene />, document.getElementById("root"));
} else {
  render(<CatalogApp />, document.getElementById("root"));
}
