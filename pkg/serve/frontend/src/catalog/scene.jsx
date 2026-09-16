import { useEffect } from "preact/hooks";
import "./zones-lab.css";
import "./scene.css";
import {
  ZonesPhone,
  ZonesDesktop,
  ZonesGrid,
  ZonesSidebar,
  StatusLineStudy,
  LiveZoneStudy,
  LIVE_STATES,
} from "./zones-lab.jsx";
import { sceneByName, SCENES } from "./scenes.js";

/* scene — one scene of the catalogue, alone, for the fidelity harness.
   `?view=scene&name=phone-working`.

   The route exists because a diff needs a frame that does not move. ?view=zones
   is the right thing to LOOK at (both densities side by side, the switches, the
   prose that explains the decisions) and the wrong thing to MEASURE: one host
   opening a drawer repaints the page, the lab prose changes when someone
   rewrites a sentence, and the whole 1400px-wide page reduces a 4px shift to a
   rounding error. Here each scene mounts one host, at its own size, with the
   lab furniture gone.

   Determinism has three parts and only one of them is here:
     - the clock and the random source: fidelity-freeze.js, imported by
       catalog-app before anything renders;
     - the animations: scene.css, plus prefers-reduced-motion, which the
       harness sets on the browser context;
     - the streaming prose: `streaming` is forced off below, so the transcript
       shows its settled text (useStream, zones-lab.jsx:819, renders the whole
       token list when not playing). A frozen clock would not be enough — the
       stream is driven by timers, not by the clock.

   The hosts are the prototype's own, imported, not copies: the thing being
   measured has to be the thing the owner accepted. */

const HOSTS = {
  phone: ({ live, surface }) => <ZonesPhone label="Phone" live={live} surface={surface} />,
  desktop: ({ live, surface }) => <ZonesDesktop label="Desktop" live={live} surface={surface} />,
  grid: ({ live, surface }) => <ZonesGrid label="Grid" live={live} surface={surface} />,
  // The list has no frame of its own in the prototype (it is the body of a
  // drawer, or the desktop column), so it gets the drawer's box at the width
  // that density gives it: 300px phone (zones-lab.css:792), 272px desktop
  // (zones-lab.css:1140).
  sidebar: ({ scene }) => (
    <div class={`fx-frame ${scene.desktop ? "is-side-desktop" : "is-side-phone"}`}>
      <ZonesSidebar
        onPick={() => {}}
        desktop={!!scene.desktop}
        onSettings={() => {}}
        view={scene.view || "recent"}
        onView={() => {}}
      />
    </div>
  ),
  statusline: () => <StatusLineStudy />,
  livezone: () => <LiveZoneStudy />,
};

function Missing({ name }) {
  return (
    <pre class="fx-missing" style="color:#f38ba8;font:14px monospace;padding:24px">
      {`unknown scene: ${name || "(none)"}\n\nknown:\n${SCENES.map((s) => "  " + s.name).join("\n")}`}
    </pre>
  );
}

export function Scene() {
  const params = new URLSearchParams(location.search);
  const name = params.get("name");
  const scene = sceneByName(name);

  useEffect(() => {
    document.documentElement.classList.add("fx-scene-root");
    return () => document.documentElement.classList.remove("fx-scene-root");
  }, []);

  if (!scene) return <Missing name={name} />;

  const host = HOSTS[scene.host];
  if (!host) return <Missing name={`${name} (host "${scene.host}")`} />;

  // The prototype's own preset objects, by id, so a scene cannot drift from
  // what the LiveSwitch offers.
  const live = LIVE_STATES.find((s) => s.id === (scene.live || "idle")) || LIVE_STATES[0];

  return (
    <div class="zl fx-scene">
      <div class="zl-aurora" aria-hidden="true" />
      {host({ live, surface: scene.surface || "none", scene })}
    </div>
  );
}
