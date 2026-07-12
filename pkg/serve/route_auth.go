package serve

import (
	"net/http"
	"strings"
)

// routeAccess is the Serve authorization policy for an HTTP route. Token
// owners retain the established Serve surface. A paired device is deliberately
// narrower: it may read the safe operational projection and use typed Pulse
// operations, but cannot reach a legacy mutation endpoint.
type routeAccess uint8

const (
	routeOwnerSurface routeAccess = iota
	routeDeviceRead
	routeDeviceOperation
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
		case routeDeviceOperation:
			if !authenticated || identity.Kind != "device" {
				http.Error(w, "Pulse operations require paired device authentication", http.StatusForbidden)
				return
			}
		case routeOwnerAdmin:
			if !authenticated || identity.Kind != "token" {
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
		case routeOwnerSurface:
			if authenticated && identity.Kind == "device" {
				http.Error(w, "paired devices must use the Pulse read or operation surface", http.StatusForbidden)
				return
			}
		case routeDeviceRead:
			// Both the owner and a paired device may read this projection. When
			// Serve has no shared token configured there cannot be a device
			// identity, so legacy unauthenticated Serve behavior remains intact.
		}
		next.ServeHTTP(w, r)
	})
}

// serveRouteAccess is the route authorization metadata. The default is the
// established owner surface, which makes adding a new mutation fail closed for
// devices until it is deliberately classified here.
func serveRouteAccess(r *http.Request) routeAccess {
	if r.Method == http.MethodPost && r.URL.Path == "/api/pulse/operations/prepare" {
		return routeDeviceOperation
	}
	if isPulseOperationRoute(r.URL.Path) && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		return routeDeviceOperation
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/pulse/pairings/claim":
		return routePairingClaim
	case r.URL.Path == "/api/pulse/pairings" && r.Method == http.MethodPost:
		return routeOwnerAdmin
	case r.URL.Path == "/api/pulse/devices" && r.Method == http.MethodGet:
		return routeOwnerAdmin
	case isPulseDeviceRevokeRoute(r.URL.Path) && r.Method == http.MethodPost:
		return routeOwnerAdmin
	case isDeviceSafeReadRoute(r):
		return routeDeviceRead
	default:
		return routeOwnerSurface
	}
}

func isPulseOperationRoute(path string) bool {
	prefix := "/api/pulse/operations/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" || strings.Contains(rest, "//") {
		return false
	}
	parts := strings.Split(rest, "/")
	return len(parts) == 1 || (len(parts) == 2 && parts[1] == "confirm")
}

func isPulseDeviceRevokeRoute(path string) bool {
	prefix := "/api/pulse/devices/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] == "revoke"
}

func isDeviceSafeReadRoute(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/api/models", "/api/fs/complete", "/api/attention", "/api/ops", "/api/ops/overview", "/api/ops/pulse", "/api/ops/ws", "/api/sessions", "/api/commands", "/api/capabilities", "/api/usage", "/api/push/vapid-public-key":
		return true
	}

	const sessionsPrefix = "/api/sessions/"
	if !strings.HasPrefix(r.URL.Path, sessionsPrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, sessionsPrefix), "/")
	if len(parts) == 1 && parts[0] != "" {
		return true
	}
	if len(parts) == 2 && parts[0] != "" {
		switch parts[1] {
		case "messages", "companion-ws", "subagents", "branches", "files", "ws":
			return true
		}
	}
	if len(parts) == 3 && parts[0] != "" {
		switch parts[1] {
		case "subagents", "files":
			return parts[2] != ""
		}
	}
	return false
}
