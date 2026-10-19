import { Inbox } from "lucide-preact";
import "./ForYou.css";

// ForYouDoor — the first row of the sidebar (and of the phone's drawer): the
// one place that says how much is waiting on you, across every session, with
// the amber count that means "waiting on you" everywhere else.
export function ForYouDoor({ n = 0, news = 0, on = false, phone = false, onOpen }) {
  return (
    <button
      type="button"
      class={`fy-door${on ? " is-on" : ""}${n > 0 ? " has-items" : ""}${phone ? " is-phone" : ""}`}
      aria-pressed={on}
      aria-label={`For you${n > 0 ? `, ${n} waiting` : ""}${news > 0 ? `, ${news} new` : ""}`}
      onClick={onOpen}
    >
      <span class="fy-door-ico"><Inbox size={16} aria-hidden="true" /></span>
      <span class="fy-door-label">For you</span>
      {news > 0 && <span class="fy-door-dot" aria-hidden="true" />}
      {n > 0 && <span class="fy-door-n zl-data">{n > 99 ? "99+" : n}</span>}
    </button>
  );
}
