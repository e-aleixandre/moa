package push

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Notification is the payload delivered (encrypted) to the service worker, which
// turns it into a system notification. Keep it minimal: it ends up on the lock
// screen, so no prompts, tool args, paths, diffs or final text — just a type and
// the session title.
type Notification struct {
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Tag       string `json:"tag,omitempty"` // coalesces same-session notifications on the device
}

const (
	// sendTimeout bounds each push HTTP call so a hung endpoint cannot block the
	// bus subscriber goroutine that triggered the notification.
	sendTimeout = 10 * time.Second
	// ttlSeconds asks the push service to hold an undelivered message this long
	// (e.g. phone offline), so a "needs you" event still arrives on reconnect.
	ttlSeconds = 3600
)

// Dispatcher sends Web Push notifications to every stored subscription.
type Dispatcher struct {
	store      *Store
	vapid      VAPID
	subscriber string // VAPID JWT "sub": a mailto: or https URL identifying the server
}

// NewDispatcher builds a dispatcher over the given store and VAPID keys.
func NewDispatcher(store *Store, vapid VAPID, subscriber string) *Dispatcher {
	return &Dispatcher{store: store, vapid: vapid, subscriber: subscriber}
}

// VAPIDPublicKey returns the public key browsers need to subscribe.
func (d *Dispatcher) VAPIDPublicKey() string { return d.vapid.Public }

// Notify fans n out to all subscriptions, pruning any the push service reports
// as gone (404/410). Best-effort: per-subscription failures are logged, not
// returned.
func (d *Dispatcher) Notify(n Notification) {
	payload, err := json.Marshal(n)
	if err != nil {
		slog.Warn("push: marshal notification", "error", err)
		return
	}
	for _, sub := range d.store.All() {
		d.send(payload, sub)
	}
}

func (d *Dispatcher) send(payload []byte, sub webpush.Subscription) {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	s := sub // SendNotificationWithContext takes a pointer; avoid aliasing the loop var
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &s, &webpush.Options{
		Subscriber:      d.subscriber,
		VAPIDPublicKey:  d.vapid.Public,
		VAPIDPrivateKey: d.vapid.Private,
		TTL:             ttlSeconds,
		Urgency:         webpush.UrgencyHigh,
	})
	if err != nil {
		slog.Warn("push: send failed", "error", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// Subscription is permanently invalid — drop it.
		if err := d.store.Remove(sub.Endpoint); err != nil {
			slog.Warn("push: prune subscription", "error", err)
		}
	case resp.StatusCode >= 300:
		slog.Warn("push: unexpected status", "status", resp.StatusCode)
	}
}
