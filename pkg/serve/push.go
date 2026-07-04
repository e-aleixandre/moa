package serve

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/push"
)

// minRunForPush gates the "finished" notification: a run must take at least this
// long to be worth a buzz. A quick answer shouldn't notify; a long run (where
// you likely stepped away) still does. Blocking events (ask/permission) and
// errors are never gated by duration.
const minRunForPush = 60 * time.Second

// subscribePush wires a session's bus to Web Push notifications following the
// trigger policy in plans/pwa-web-push-plan.md §D3:
//
//   - ask_user / permission → always (blocking events; the agent is waiting on
//     you, and missing one costs more than a redundant buzz).
//   - run finished OK → only when no browser is watching the session live
//     (wsConns == 0) AND the run lasted at least minRunForPush, so quick answers
//     and turns you're looking at don't buzz.
//   - run errored → only when no browser is watching the session live.
//
// Errors dedupe to one source: success comes from RunEnded{Err==nil}, failure
// from StateChanged("error") — never both, so an errored run notifies once.
//
// The unsubscribe funcs are stored on the session and invoked by Delete BEFORE
// the runtime closes, so an event drained during shutdown cannot notify for a
// session that is already gone (the deleted guard is belt-and-suspenders).
func (m *Manager) subscribePush(sess *ManagedSession) {
	if m.pushDispatcher == nil {
		return
	}
	b := sess.runtime.Bus

	// notify shows the action as the Title; the Body is only ever the session
	// title (the "which session"), never the specifics. Notifications land on
	// the device lock screen and in the OS notification history, so — per the
	// push.Notification contract — they must not carry prompts, tool
	// args/commands, paths, diffs, final text or error detail. E2E encryption
	// hides the payload from the push service but NOT from the lock screen, so
	// the content is dropped here; open the app to see it.
	notify := func(title string) {
		if sess.deleted.Load() {
			return
		}
		m.pushDispatcher.Notify(push.Notification{
			Title:     title,
			Body:      sess.title(),
			SessionID: sess.ID,
			Tag:       sess.ID, // coalesce same-session notifications on the device
		})
	}
	notifyIfAway := func(title string) {
		if sess.wsConns.Load() == 0 {
			notify(title)
		}
	}

	// runStartNano records when the current run began so RunEnded can gate the
	// "finished" push on duration. RunStarted and RunEnded are handled on
	// separate subscriber goroutines, so the timestamp is shared atomically; in
	// practice RunStarted is processed long before RunEnded (a run takes real
	// time), and if the start is somehow unknown we fail open and notify.
	var runStartNano atomic.Int64

	sess.pushUnsubs = append(sess.pushUnsubs,
		b.Subscribe(func(bus.AskUserRequested) {
			notify("moa necesita tu decisión")
		}),
		b.Subscribe(func(bus.PermissionRequested) {
			notify("moa espera tu aprobación")
		}),
		b.Subscribe(func(bus.RunStarted) { runStartNano.Store(time.Now().UnixNano()) }),
		b.Subscribe(func(e bus.RunEnded) {
			if e.Err != nil {
				return
			}
			if start := runStartNano.Load(); start != 0 && time.Since(time.Unix(0, start)) < minRunForPush {
				return // quick answer — not worth a buzz
			}
			notifyIfAway("moa terminó")
		}),
		b.Subscribe(func(e bus.StateChanged) {
			if e.State == string(bus.StateError) {
				notifyIfAway("moa falló")
			}
		}),
	)
}

// handlePushVAPIDKey returns the server's VAPID public key so the browser can
// subscribe. GET → no X-Moa-Request header required.
func handlePushVAPIDKey(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.pushDispatcher == nil {
			http.Error(w, "push not available", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"key": mgr.pushDispatcher.VAPIDPublicKey()})
	}
}

// handlePushSubscribe stores a browser's Web Push subscription.
func handlePushSubscribe(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.pushStore == nil {
			http.Error(w, "push not available", http.StatusServiceUnavailable)
			return
		}
		limitBody(w, r, maxJSONBodySize)
		var sub webpush.Subscription
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
			slog.Warn("push: subscribe decode failed", "error", err)
			http.Error(w, "invalid subscription", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(sub.Endpoint, "https://") || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
			slog.Warn("push: subscribe rejected",
				"endpoint_https", strings.HasPrefix(sub.Endpoint, "https://"),
				"has_p256dh", sub.Keys.P256dh != "", "has_auth", sub.Keys.Auth != "")
			http.Error(w, "invalid subscription", http.StatusBadRequest)
			return
		}
		if err := mgr.pushStore.Add(sub); err != nil {
			http.Error(w, "could not store subscription", http.StatusInternalServerError)
			return
		}
		slog.Info("push: subscription stored", "total", mgr.pushStore.Len())
		w.WriteHeader(http.StatusNoContent)
	}
}

// handlePushUnsubscribe removes a subscription by endpoint.
func handlePushUnsubscribe(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.pushStore == nil {
			http.Error(w, "push not available", http.StatusServiceUnavailable)
			return
		}
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := mgr.pushStore.Remove(body.Endpoint); err != nil {
			http.Error(w, "could not remove subscription", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
