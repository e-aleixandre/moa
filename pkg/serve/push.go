package serve

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/session"
)

// minRunForPush (a var so tests need not wait a minute) gates the "finished" notification: a run must take at least this
// long to be worth a buzz. A quick answer shouldn't notify; a long run (where
// you likely stepped away) still does. Blocking events (ask/permission) and
// errors are never gated by duration.
var minRunForPush = 60 * time.Second

// subscribePush feeds a session's bus to the push policy (pkg/push/policy.go,
// where every rule lives). This only says what happened and what is known
// about it: who is looking, whether the run was a report being digested.
//
// Each trigger dedupes to one source: success comes from RunEnded{Err==nil,
// !Cancelled}, failure from StateChanged("error") — never both, and a run the
// user cancelled says nothing.
//
// The unsubscribe funcs are stored on the session and invoked by Delete BEFORE
// the runtime closes, so an event drained during shutdown cannot notify for a
// session that is already gone (the deleted guard is belt-and-suspenders).
func (m *Manager) subscribePush(sess *ManagedSession) {
	if m.pushPolicy == nil {
		return
	}
	pol := m.pushPolicy
	b := sess.runtime.Bus

	// Only the action is the Title and only the session title the Body, never
	// the specifics: notifications land on the device lock screen and in the OS
	// notification history, so — per the push.Notification contract — they must
	// not carry prompts, tool args/commands, paths, diffs, final text or error
	// detail. Open the app to see it.
	//
	// A session an owner launched (origin "owner", not the owner's own
	// conversation) never pushes: its owner hears about it through its
	// reports. Origin and Kind are assigned after the session is built, so they
	// are read here, when an event arrives, not at subscription time.
	allowed := func() bool {
		if sess.deleted.Load() {
			return false
		}
		return sess.Origin != "owner" || sess.Kind == session.KindOwner
	}
	signal := func(kind push.Kind) push.Signal {
		return push.Signal{
			Kind:      kind,
			SessionID: sess.ID,
			Title:     sess.title(),
			Watched:   sess.presence.watched(),
		}
	}
	// A question or permission someone was looking at waits out a grace period;
	// it is sent only if that very request is still open, which is also checked
	// before an immediate send (the request may have been answered while this
	// event waited its turn).
	request := func(kind push.Kind, id string) {
		if !allowed() {
			return
		}
		approvals := sess.runtime.Context().Approvals
		s := signal(kind)
		s.RequestID = id
		s.StillPending = func() bool {
			if !allowed() {
				return false
			}
			if kind == push.KindAsk {
				return approvals.AskPending(id)
			}
			return approvals.PermissionPending(id)
		}
		pol.Handle(s)
	}

	// One subscriber sees the run's events in the order they happened, so what
	// a run was (a digest of a report, how long it took) is learnt before the
	// run ends and cannot be mixed up with the next run's.
	var run struct {
		active bool
		gen    uint64
		start  time.Time
		digest bool
	}
	// An input that lands in a digest run and did not come from a report (the
	// user's own instruction) makes it a turn somebody asked for.
	landed := func(custom map[string]any) {
		if run.active && run.digest {
			if o := bus.OriginOfInput(custom); o.Explicit && o.Source != reportSource {
				run.digest = false
			}
		}
	}

	sess.pushUnsubs = append(sess.pushUnsubs,
		func() { pol.CancelSession(sess.ID) },
		b.SubscribeAll(func(event any) {
			switch e := event.(type) {
			case bus.AskUserRequested:
				request(push.KindAsk, e.ID)
			case bus.PermissionRequested:
				request(push.KindPermission, e.ID)
			case bus.AskUserResolved:
				pol.Resolved(push.KindAsk, sess.ID, e.ID)
			case bus.PermissionResolved:
				pol.Resolved(push.KindPermission, sess.ID, e.ID)
			case bus.RunStarted:
				run.active, run.gen, run.start = true, e.RunGen, time.Now()
				run.digest = e.Origin.Source == reportSource
			case bus.Steered:
				landed(e.Custom)
			case bus.UserMessageAppended:
				landed(e.Custom)
			case bus.RunEnded:
				known := run.active && run.gen == e.RunGen
				start, digest := run.start, known && run.digest
				run.active = false
				if e.Err != nil || e.Cancelled || !allowed() {
					return
				}
				switch {
				case digest:
					s := signal(push.KindDigest)
					s.Project = sess.CWD
					pol.Handle(s)
				case known && time.Since(start) < minRunForPush:
					// quick answer — not worth a buzz
				default:
					pol.Handle(signal(push.KindDone))
				}
			case bus.StateChanged:
				if e.State == string(bus.StateError) && allowed() {
					pol.Handle(signal(push.KindFailed))
				}
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
