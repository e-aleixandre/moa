import { useState, useEffect } from "preact/hooks";
import { BellRing, Volume2, VolumeX, Check } from "lucide-preact";
import { toggleSound } from "../../data/tile-actions.js";
import {
  getPushState, subscribePushState, enablePush, disablePush,
} from "../../data/push-client.js";
import "./NotificationSettings.css";

// Sub-labels for the push row, keyed by push-client state.
const PUSH_SUB = {
  unsupported: "Not available in this browser",
  default: "Off",
  denied: "Blocked — enable in settings",
  subscribed: "On",
  busy: "Applying…",
};

// NotificationSettings — device-wide notifications dropdown (push + sound). It
// renders ONLY the dropdown body; the parent owns the anchor/trigger and the
// open state (see ChatHead's onNotifications + the popover pattern in
// ConversationScreen / MobileHeader), so it composes with the other head
// popovers (model / session settings) and gets click-outside + Escape from the
// same registerOverlay wiring.
//
// Push is the primary channel — it reaches the phone with the app closed; sound
// is the in-page beep for when a tab is open but hidden. Sound state lives in
// the store (soundEnabled, persisted) via toggleSound, matching how the next
// keeps its other prefs.
export function NotificationSettings({ soundEnabled }) {
  const [push, setPush] = useState(getPushState());
  useEffect(() => subscribePushState(setPush), []);

  const pushOn = push === "subscribed";
  const pushDisabled = push === "unsupported" || push === "denied" || push === "busy";

  const onPushClick = () => {
    if (push === "subscribed") disablePush();
    else if (push === "default") enablePush();
    // denied / unsupported / busy → no-op (the sub-label explains why)
  };

  return (
    <div class="notif-dropdown">
      <div class="notif-title">Notifications</div>

      <button
        type="button"
        class={`notif-row ${pushOn ? "active" : ""}`}
        onClick={onPushClick}
        disabled={pushDisabled}
      >
        <BellRing class="notif-row-icon" size={16} />
        <span class="notif-row-text">
          <span class="notif-row-title">Push</span>
          <span class="notif-row-sub">{PUSH_SUB[push]}</span>
        </span>
        {pushOn && <Check class="notif-row-check" size={14} />}
      </button>

      <button
        type="button"
        class={`notif-row ${soundEnabled ? "active" : ""}`}
        onClick={toggleSound}
      >
        {soundEnabled
          ? <Volume2 class="notif-row-icon" size={16} />
          : <VolumeX class="notif-row-icon" size={16} />}
        <span class="notif-row-text">
          <span class="notif-row-title">Sound</span>
          <span class="notif-row-sub">{soundEnabled ? "On" : "Off"}</span>
        </span>
        {soundEnabled && <Check class="notif-row-check" size={14} />}
      </button>
    </div>
  );
}
