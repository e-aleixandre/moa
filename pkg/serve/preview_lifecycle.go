package serve

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// PreviewSettings is what Moa remembers about the Live Preview proxy between
// runs: how the browser reaches the listener, and which port it binds. Whether
// the proxy is RUNNING is never persisted — activation is per use, in memory,
// so a restart leaves no port open until someone opens a preview again.
type PreviewSettings struct {
	PublicURL string
	Port      int
}

// PreviewStore persists PreviewSettings. Every error is reported to the caller
// and degrades the preview only: Moa itself must keep running.
type PreviewStore interface {
	Load() (PreviewSettings, error)
	Save(PreviewSettings) error
}

// globalPreviewStore keeps the settings in the user's global moa config
// (~/.config/moa/config.json), never in the repository or in a session.
type globalPreviewStore struct{}

// GlobalPreviewStore returns the PreviewStore backed by the global moa config.
func GlobalPreviewStore() PreviewStore { return globalPreviewStore{} }

func (globalPreviewStore) Load() (PreviewSettings, error) {
	cfg := core.LoadGlobalConfig()
	if cfg.Preview == nil {
		return PreviewSettings{}, nil
	}
	return PreviewSettings{PublicURL: cfg.Preview.PublicURL, Port: cfg.Preview.Port}, nil
}

func (globalPreviewStore) Save(s PreviewSettings) error {
	return core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		cfg.Preview = &core.PreviewConfig{PublicURL: s.PublicURL, Port: s.Port}
	})
}

// PreviewController owns the lifetime of the Live Preview listener.
//
// The listener is created on demand — when someone actually loads a URL in the
// preview panel — and destroyed on demand. With no preview in use Moa opens no
// extra port at all, which is exactly the posture of a Moa started without the
// preview flags. That is the whole point: turning the preview on must not cost
// a restart of a server that is running six agent sessions.
type PreviewController struct {
	moaPort      int
	allowedHosts []string
	store        PreviewStore

	mu         sync.Mutex
	settings   PreviewSettings
	storeErr   string
	proxy      *PreviewProxy
	server     *http.Server
	listener   net.Listener
	activePort int
	activeURL  string
}

// NewPreviewController loads the remembered public URL and port. A store that
// cannot be read is recorded and surfaced through the API; it never aborts
// startup, because a bad config file must not take Moa down with it.
func NewPreviewController(moaPort int, allowedHosts []string, store PreviewStore) *PreviewController {
	c := &PreviewController{moaPort: moaPort, allowedHosts: append([]string(nil), allowedHosts...), store: store}
	if store != nil {
		settings, err := store.Load()
		if err != nil {
			c.storeErr = fmt.Sprintf("could not read the saved preview settings: %v", err)
		} else {
			c.settings = settings
		}
	}
	return c
}

// Configure sets the address without opening anything. It is what the startup
// flags do: they are initial configuration, not activation, so passing them
// still costs no open port until a preview is actually used.
func (c *PreviewController) Configure(publicURL string, port int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = PreviewSettings{PublicURL: strings.TrimRight(strings.TrimSpace(publicURL), "/"), Port: port}
	c.storeErr = ""
}

// SuggestedPort is the default port for the proxy listener: the port right
// above the one Moa itself serves on. It is derived rather than fixed so two
// Moa instances (8080 and 7401, say) never propose the same preview port, and
// so the user can predict it from the address they already type. It is only a
// proposal: binding is what decides, and a busy port fails loudly.
func (c *PreviewController) SuggestedPort() int {
	if c.moaPort <= 0 || c.moaPort >= 65535 {
		return 7492
	}
	return c.moaPort + 1
}

// PreviewStatus is the API view of the proxy: what is configured, and what is
// actually listening right now. "enabled" means a bound listener, never an
// intention — reporting an active listener that is not there is the one lie
// that would leave the user staring at a preview that can never load.
type PreviewStatus struct {
	Enabled       bool
	PublicURL     string
	Port          int
	SuggestedPort int
	Error         string
}

func (c *PreviewController) Status() PreviewStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := PreviewStatus{
		Enabled:       c.proxy != nil,
		PublicURL:     c.settings.PublicURL,
		Port:          c.settings.Port,
		SuggestedPort: c.SuggestedPort(),
		Error:         c.storeErr,
	}
	if status.Port == 0 {
		status.Port = c.SuggestedPort()
	}
	if status.Enabled {
		status.PublicURL = c.activeURL
		status.Port = c.activePort
	}
	return status
}

// Proxy returns the running proxy, or nil when the listener is down.
func (c *PreviewController) Proxy() *PreviewProxy {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proxy
}

// Activate brings the listener up, binding synchronously so a busy port is an
// error to the caller rather than a goroutine writing to stderr. Returns the
// live proxy and whether this call is the one that started it (so a caller that
// then fails to set a target can undo exactly what it caused).
func (c *PreviewController) Activate(publicURL string, port int) (*PreviewProxy, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		publicURL = c.settings.PublicURL
	}
	origin, err := normalizedOrigin(publicURL)
	if err != nil {
		return nil, false, errors.New("the preview address must be an absolute http(s) URL, e.g. https://your-host:7492")
	}
	if port == 0 {
		port = portFromOrigin(origin)
	}
	if port == 0 {
		port = c.settings.Port
	}
	if port == 0 {
		port = c.SuggestedPort()
	}
	if port < 1 || port > 65535 {
		return nil, false, errors.New("the preview port must be between 1 and 65535")
	}
	if port == c.moaPort {
		return nil, false, errors.New("the preview port must differ from the port Moa serves on")
	}

	if c.proxy != nil {
		if c.activeURL == origin && c.activePort == port {
			return c.proxy, false, nil
		}
		c.stopLocked()
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, false, fmt.Errorf("port %d is not available for the preview proxy: %w", port, err)
	}
	proxy := NewPreviewProxy(origin, c.moaPort, port)
	server := &http.Server{
		Handler:           proxy.ProtectedHandler(c.allowedHosts),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()

	c.proxy, c.server, c.listener, c.activeURL, c.activePort = proxy, server, listener, origin, port
	c.remember(PreviewSettings{PublicURL: origin, Port: port})
	return proxy, true, nil
}

// remember persists the address only once a listener is actually bound, so a
// port that cannot be opened is never written back as the saved default.
func (c *PreviewController) remember(s PreviewSettings) {
	if c.store == nil || (c.settings == s && c.storeErr == "") {
		c.settings = s
		return
	}
	c.settings = s
	if err := c.store.Save(s); err != nil {
		c.storeErr = fmt.Sprintf("could not save the preview settings: %v", err)
		return
	}
	c.storeErr = ""
}

// Deactivate tears the listener down and severs everything in flight: pending
// upstream dials, established upstream connections, and any client connection
// (including HMR websocket upgrades) still parked on the listener.
func (c *PreviewController) Deactivate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
}

func (c *PreviewController) stopLocked() {
	if c.proxy == nil {
		return
	}
	// Upstream first: Close cancels in-progress dials and drops every tracked
	// connection, so no late dial can outlive the shutdown.
	c.proxy.Close()
	// The listener is closed directly as well as through the server. Close only
	// closes listeners Serve has already registered, and Serve runs on another
	// goroutine: without this, an activate/deactivate/activate burst can leave
	// the port bound and the next activation fails with "address already in use".
	_ = c.listener.Close()
	_ = c.server.Close()
	c.proxy, c.server, c.listener, c.activeURL, c.activePort = nil, nil, nil, "", 0
}

// Close is the process-shutdown path.
func (c *PreviewController) Close() { c.Deactivate() }

func portFromOrigin(origin string) int {
	u, err := url.Parse(origin)
	if err != nil {
		return 0
	}
	if p := u.Port(); p != "" {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}
