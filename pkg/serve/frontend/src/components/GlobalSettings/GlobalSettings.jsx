import { NotificationSettings } from "../NotificationSettings/NotificationSettings.jsx";
import { SubagentModels } from "./SubagentModels.jsx";
import "./GlobalSettings.css";

// GlobalSettings — device-wide settings body shared by desktop (Sheet from the
// spine ⚙) and mobile (bottom-sheet from the drawer footer). Global scope only:
// no per-session rows. Parents own the shell (Sheet / MobileSheet) and open
// state; this owns the content so both densities stay in parity.
//
// Today: Notifications (push + sound), Subagents (which models delegation may
// use) and About (version). Expand here rather than forking desktop/mobile
// bodies again. Server-side settings live here too: this is THE global
// settings surface, regardless of where the value is stored.
export function GlobalSettings({ soundEnabled, version = null }) {
  const current = version?.current || null;
  const latest = version?.latest || null;
  const updateAvailable = !!(version?.update_available && latest);

  return (
    <div class="global-settings">
      <section class="global-settings-section">
        <div class="global-settings-lbl">Notifications</div>
        <NotificationSettings soundEnabled={soundEnabled} />
      </section>

      <section class="global-settings-section">
        <div class="global-settings-lbl">Subagents</div>
        <SubagentModels />
      </section>

      <section class="global-settings-section global-settings-about">
        <div class="global-settings-lbl">About</div>
        <div class="global-settings-about-row">
          <span class="global-settings-about-key">Version</span>
          {current ? (
            updateAvailable ? (
              <span
                class="global-settings-about-val global-settings-about-update"
                title={`Update available: ${latest}`}
              >
                {current} ↑ {latest}
              </span>
            ) : (
              <span class="global-settings-about-val" title="moa version">
                {current}
              </span>
            )
          ) : (
            <span class="global-settings-about-val global-settings-about-muted">—</span>
          )}
        </div>
      </section>
    </div>
  );
}
