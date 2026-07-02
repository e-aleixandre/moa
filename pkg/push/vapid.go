package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// VAPID is the server's VAPID key pair (RFC 8292). The public key is shared with
// browsers (application server key); the private key signs the VAPID JWT and must
// stay secret (persisted 0600).
type VAPID struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

// LoadOrGenerateVAPID reads the VAPID key pair from path, generating and
// persisting a new pair (0600) on first use. The pair is stable across restarts
// so existing browser subscriptions keep working.
func LoadOrGenerateVAPID(path string) (VAPID, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var v VAPID
		if err := json.Unmarshal(data, &v); err != nil {
			return VAPID{}, fmt.Errorf("parse vapid file %s: %w", path, err)
		}
		if v.Public == "" || v.Private == "" {
			return VAPID{}, fmt.Errorf("vapid file %s is missing keys", path)
		}
		return v, nil
	case errors.Is(err, os.ErrNotExist):
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			return VAPID{}, fmt.Errorf("generate vapid keys: %w", err)
		}
		v := VAPID{Public: pub, Private: priv}
		out, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return VAPID{}, err
		}
		if err := writeFileAtomic(path, out, 0o600); err != nil {
			return VAPID{}, fmt.Errorf("persist vapid keys: %w", err)
		}
		return v, nil
	default:
		return VAPID{}, fmt.Errorf("read vapid file %s: %w", path, err)
	}
}
