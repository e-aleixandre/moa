package serve

import (
	"net/http"
	"strings"
)

// routeAccess is the Serve authorization policy for an HTTP route. Token
// owners and paired devices share the generic Serve API. Pairing administration
// remains owner-only so an already paired device cannot extend its authority.
type routeAccess uint8

const (
	routeOwnerSurface routeAccess = iota
	routeOwnerAdmin
	routePairingClaim
)

// routeAuthorizationMiddleware is kept separate from authentication so route
// policy is declared in one place rather than distributed through handlers.
// Authentication establishes the identity; this middleware determines which
// surface that identity can use.
func routeAuthorizationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access := serveRouteAccess(r)
		identity, authenticated := requestAuthIdentity(r)

		switch access {
		case routeOwnerAdmin:
			if !authenticated || (identity.Kind != "token" && identity.Kind != "network") {
				http.Error(w, "paired devices cannot administer pairing", http.StatusForbidden)
				return
			}
		case routePairingClaim:
			// Claim is intentionally unauthenticated because the short-lived QR
			// secret is its authority. A paired device may not use this route to
			// turn a captured pairing payload into a new device credential.
			if authenticated && identity.Kind == "device" {
				http.Error(w, "paired devices cannot claim pairings", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// serveRouteAccess reserves pairing administration to the owner. Every other
// generic Serve route is available to an authenticated paired device.
func serveRouteAccess(r *http.Request) routeAccess {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/pulse/pairings/claim":
		return routePairingClaim
	case r.URL.Path == "/api/pulse/pairings" && r.Method == http.MethodPost:
		return routeOwnerAdmin
	case r.URL.Path == "/api/pulse/devices" && r.Method == http.MethodGet:
		return routeOwnerAdmin
	case isPulseDeviceRevokeRoute(r.URL.Path) && r.Method == http.MethodPost:
		return routeOwnerAdmin
	default:
		return routeOwnerSurface
	}
}

func isPulseDeviceRevokeRoute(path string) bool {
	prefix := "/api/pulse/devices/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] == "revoke"
}
