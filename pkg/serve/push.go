package serve

import (
	"encoding/json"
	"net/http"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/push"
)

// subscribePush wires a session's bus to Web Push notifications following the
// trigger policy in plans/pwa-web-push-plan.md §D3:
//
//   - ask_user / permission → always (blocking events; the agent is waiting on
//     you, and missing one costs more than a redundant buzz).
//   - run finished OK / errored → only when no browser is watching the session
//     live (wsConns == 0), so interactive turns you're looking at don't buzz.
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

	sess.pushUnsubs = append(sess.pushUnsubs,
		b.Subscribe(func(bus.AskUserRequested) { notify("moa necesita tu decisión") }),
		b.Subscribe(func(bus.PermissionRequested) { notify("moa espera tu aprobación") }),
		b.Subscribe(func(e bus.RunEnded) {
			if e.Err == nil {
				notifyIfAway("moa terminó")
			}
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
			http.Error(w, "invalid subscription", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(sub.Endpoint, "https://") || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
			http.Error(w, "invalid subscription", http.StatusBadRequest)
			return
		}
		if err := mgr.pushStore.Add(sub); err != nil {
			http.Error(w, "could not store subscription", http.StatusInternalServerError)
			return
		}
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
