package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// memoryPreviewStore is the persistence seam, not a stand-in for the real
// config file: TestPreviewSettingsPersistInTheGlobalConfig exercises that one.
type memoryPreviewStore struct {
	mu       sync.Mutex
	settings PreviewSettings
	loadErr  error
	saveErr  error
	saves    int
}

func (s *memoryPreviewStore) Load() (PreviewSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, s.loadErr
}

func (s *memoryPreviewStore) Save(v PreviewSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.settings = v
	s.saves++
	return nil
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func listening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// The headline behavior: no port until someone previews, a real port while they
// do, and nothing left behind afterwards — with real listeners, not fakes.
func TestPreviewControllerOpensAndClosesARealListener(t *testing.T) {
	port := freePort(t)
	store := &memoryPreviewStore{}
	c := NewPreviewController(7401, []string{"dev.test"}, store)
	t.Cleanup(c.Close)

	if listening(port) {
		t.Fatal("something was already listening before activation")
	}
	if status := c.Status(); status.Enabled {
		t.Fatal("a fresh controller reported an active listener")
	}

	proxy, started, err := c.Activate("http://dev.test:"+strconv.Itoa(port), 0)
	if err != nil || proxy == nil || !started {
		t.Fatalf("activate: proxy=%v started=%v err=%v", proxy, started, err)
	}
	if !listening(port) {
		t.Fatal("activation did not bind the port")
	}
	if status := c.Status(); !status.Enabled || status.Port != port {
		t.Fatalf("status after activation = %+v", status)
	}
	// The address is remembered so the UI never asks twice; "running" is not.
	if store.settings.PublicURL != "http://dev.test:"+strconv.Itoa(port) || store.settings.Port != port {
		t.Fatalf("settings not persisted: %+v", store.settings)
	}

	c.Deactivate()
	if listening(port) {
		t.Fatal("deactivation left the port open")
	}
	if status := c.Status(); status.Enabled {
		t.Fatal("status reported an active listener after deactivation")
	}

	// Reactivation is a fresh listener on the same port, not a resurrected one.
	if _, _, err := c.Activate("http://dev.test:"+strconv.Itoa(port), 0); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if !listening(port) {
		t.Fatal("reactivation did not bind the port again")
	}
}

// A busy port must be an error to the caller. Reporting success here is the one
// failure that would leave the user waiting on a preview that cannot exist.
func TestPreviewControllerFailsLoudlyOnABusyPort(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close() }()
	port := blocker.Addr().(*net.TCPAddr).Port

	c := NewPreviewController(7401, nil, &memoryPreviewStore{})
	t.Cleanup(c.Close)
	proxy, started, err := c.Activate("http://dev.test:"+strconv.Itoa(port), 0)
	if err == nil {
		t.Fatal("a busy port was accepted")
	}
	if proxy != nil || started {
		t.Fatalf("a failed activation returned a proxy: proxy=%v started=%v", proxy, started)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("the error does not name the port: %v", err)
	}
	if status := c.Status(); status.Enabled {
		t.Fatal("a failed activation reported an active listener")
	}
}

func TestPreviewControllerRejectsUnusableAddresses(t *testing.T) {
	c := NewPreviewController(7401, nil, &memoryPreviewStore{})
	t.Cleanup(c.Close)
	for _, address := range []string{"", "not a url", "ftp://dev.test:7402", "dev.test:7402", "http://dev.test:7402/some/path"} {
		if _, _, err := c.Activate(address, 0); err == nil {
			t.Errorf("accepted %q as a preview address", address)
		}
	}
	// Publishing the proxy on Moa's own port would proxy Moa to itself.
	if _, _, err := c.Activate("http://dev.test:7401", 7401); err == nil {
		t.Error("accepted moa's own port for the preview listener")
	}
}

// A request in flight when the preview is turned off must not survive it: the
// client connection is severed and the upstream connection is dropped.
func TestPreviewDeactivationSeversRequestsInFlight(t *testing.T) {
	release := make(chan struct{})
	var upstreamConns sync.WaitGroup
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer upstream.Close()
	defer close(release)
	upstreamConns.Wait()

	port := freePort(t)
	c := NewPreviewController(7401, nil, &memoryPreviewStore{})
	t.Cleanup(c.Close)
	proxy, _, err := c.Activate("http://127.0.0.1:"+strconv.Itoa(port), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.SetTarget(upstream.URL, nil); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{}
	request, _ := http.NewRequest("GET", proxy.PreviewURL(), nil)
	request.URL.Host = "127.0.0.1:" + strconv.Itoa(port)
	request.Host = "127.0.0.1:" + strconv.Itoa(port)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	grant, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = grant.Body.Close()
	cookie := findCookie(grant.Cookies(), previewAuthCookie)
	if cookie == nil {
		t.Fatal("no preview cookie was issued")
	}

	inFlight := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/slow", nil)
		req.AddCookie(cookie)
		resp, err := client.Do(req)
		if err != nil {
			inFlight <- err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, err = io.ReadAll(resp.Body)
		inFlight <- err
	}()

	// Wait until the request is actually parked upstream before pulling the rug.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if proxy.targetSnapshot() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	c.Deactivate()

	select {
	case err := <-inFlight:
		if err == nil {
			t.Fatal("the in-flight request completed normally after deactivation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request outlived deactivation")
	}
	if listening(port) {
		t.Fatal("the listener survived deactivation")
	}
}

// After deactivate → activate, the credentials the browser still holds from the
// previous cycle must be worthless.
func TestPreviewCycleInvalidatesOldCredentials(t *testing.T) {
	port := freePort(t)
	c := NewPreviewController(7401, []string{"dev.test"}, &memoryPreviewStore{})
	t.Cleanup(c.Close)
	address := "http://dev.test:" + strconv.Itoa(port)

	first, _, err := c.Activate(address, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldPreviewURL := first.PreviewURL()
	protected := first.ProtectedHandler([]string{"dev.test"})
	grant := httptest.NewRecorder()
	request := httptest.NewRequest("GET", oldPreviewURL, nil)
	request.Host = "dev.test"
	protected.ServeHTTP(grant, request)
	oldCookie := findCookie(grant.Result().Cookies(), previewAuthCookie)
	if oldCookie == nil {
		t.Fatal("no cookie issued in the first cycle")
	}

	c.Deactivate()
	second, _, err := c.Activate(address, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("reactivation reused the closed proxy")
	}
	newProtected := second.ProtectedHandler([]string{"dev.test"})
	for name, request := range map[string]*http.Request{
		"old capability URL": httptest.NewRequest("GET", oldPreviewURL, nil),
		"old auth cookie":    httptest.NewRequest("GET", "http://dev.test/", nil),
	} {
		request.Host = "dev.test"
		request.AddCookie(oldCookie)
		response := httptest.NewRecorder()
		newProtected.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s accepted after a deactivate/activate cycle: %d", name, response.Code)
		}
	}
	// The closed proxy itself must refuse everything too, cookie or not.
	closedResponse := httptest.NewRecorder()
	closedRequest := httptest.NewRequest("GET", "http://dev.test/", nil)
	closedRequest.Host = "dev.test"
	closedRequest.AddCookie(oldCookie)
	first.ProtectedHandler([]string{"dev.test"}).ServeHTTP(closedResponse, closedRequest)
	if closedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("the closed proxy still served requests: %d", closedResponse.Code)
	}
}

// A dial that completes after Close must be closed, not handed to a request.
func TestPreviewCloseDropsALateDial(t *testing.T) {
	p := NewPreviewProxy("https://dev.test:7402", 7401, 7402)
	if err := p.SetTarget("http://127.0.0.1:5173", nil); err != nil {
		t.Fatal(err)
	}
	target := p.targetSnapshot()
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	dialing := make(chan struct{})
	p.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialing)
		<-ctx.Done()
		return client, nil
	}

	result := make(chan error, 1)
	go func() {
		ctx := context.WithValue(context.Background(), previewDialPlanKey{}, previewDialPlan{ips: target.ips, port: target.port, gen: target.generation})
		conn, err := p.dialContext(ctx, "tcp", "ignored")
		if err == nil && conn != nil {
			result <- errors.New("a dial completed after Close and was handed back")
			return
		}
		result <- nil
	}()
	<-dialing
	p.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel the dial in flight")
	}
	_ = peer.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := peer.Write([]byte("x")); err == nil {
		t.Fatal("the late connection was left open")
	}
}

// Two sessions activating and deactivating at once must not corrupt the
// controller or leave a stray listener.
func TestPreviewControllerConcurrentActivation(t *testing.T) {
	port := freePort(t)
	c := NewPreviewController(7401, nil, &memoryPreviewStore{})
	t.Cleanup(c.Close)
	address := "http://127.0.0.1:" + strconv.Itoa(port)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _, _ = c.Activate(address, 0)
				return
			}
			c.Deactivate()
		}(i)
	}
	wg.Wait()
	c.Deactivate()
	if listening(port) {
		t.Fatal("a listener survived the concurrent churn")
	}
	if status := c.Status(); status.Enabled {
		t.Fatalf("status after the churn = %+v", status)
	}
}

// A store that cannot be read or written degrades the preview and nothing else.
func TestPreviewStoreFailuresDoNotBreakActivation(t *testing.T) {
	unreadable := NewPreviewController(7401, nil, &memoryPreviewStore{loadErr: errors.New("permission denied")})
	t.Cleanup(unreadable.Close)
	if status := unreadable.Status(); status.Error == "" || status.Enabled {
		t.Fatalf("an unreadable store was not surfaced: %+v", status)
	}
	port := freePort(t)
	if _, _, err := unreadable.Activate("http://127.0.0.1:"+strconv.Itoa(port), 0); err != nil {
		t.Fatalf("an unreadable store blocked activation: %v", err)
	}
	if !listening(port) {
		t.Fatal("an unreadable store prevented the listener from binding")
	}

	unwritable := NewPreviewController(7401, nil, &memoryPreviewStore{saveErr: errors.New("read-only file system")})
	t.Cleanup(unwritable.Close)
	other := freePort(t)
	if _, _, err := unwritable.Activate("http://127.0.0.1:"+strconv.Itoa(other), 0); err != nil {
		t.Fatalf("an unwritable store blocked activation: %v", err)
	}
	status := unwritable.Status()
	if !status.Enabled {
		t.Fatal("an unwritable store prevented the preview from running")
	}
	if status.Error == "" {
		t.Fatal("a failed save was not reported to the user")
	}
}

// The suggested port is deterministic and derived from Moa's own port, so two
// instances never propose the same one.
func TestPreviewSuggestedPortIsDerivedFromMoaPort(t *testing.T) {
	for moaPort, want := range map[int]int{8080: 8081, 7401: 7402, 0: 7492, 65535: 7492} {
		c := NewPreviewController(moaPort, nil, nil)
		if got := c.SuggestedPort(); got != want {
			t.Errorf("moa port %d suggested %d, want %d", moaPort, got, want)
		}
	}
}

// The remembered address lives in the user's global moa config — never in the
// repository, never in a session — and survives a restart of the controller.
func TestPreviewSettingsPersistInTheGlobalConfig(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", configDir)

	port := freePort(t)
	address := "http://127.0.0.1:" + strconv.Itoa(port)
	first := NewPreviewController(7401, nil, GlobalPreviewStore())
	if _, _, err := first.Activate(address, 0); err != nil {
		t.Fatal(err)
	}
	first.Close()

	raw, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatalf("the preview address was not written to the global config: %v", err)
	}
	if !strings.Contains(string(raw), address) {
		t.Fatalf("config.json does not carry the address: %s", raw)
	}
	// Activation is per use: nothing in the file may make a restart reopen it.
	if strings.Contains(string(raw), "\"enabled\"") {
		t.Fatalf("activation state was persisted: %s", raw)
	}

	second := NewPreviewController(7401, nil, GlobalPreviewStore())
	t.Cleanup(second.Close)
	status := second.Status()
	if status.PublicURL != address || status.Port != port {
		t.Fatalf("the saved address was not restored: %+v", status)
	}
	if status.Enabled || listening(port) {
		t.Fatal("a restarted controller opened the port on its own")
	}
}

// The API is the contract the web UI depends on: GET never claims a listener
// that is not there, PUT starts one, and PUT {enabled:false} takes it down.
func TestPreviewTargetAPIActivatesAndDeactivates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<head></head>")
	}))
	defer upstream.Close()

	port := freePort(t)
	c := NewPreviewController(7401, nil, &memoryPreviewStore{})
	t.Cleanup(c.Close)
	handler := handlePreviewTarget(c)

	idle := httptest.NewRecorder()
	handler(idle, httptest.NewRequest("GET", "/api/preview/target", nil))
	var status map[string]any
	if err := json.Unmarshal(idle.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["enabled"] != false {
		t.Fatalf("an idle controller reported enabled: %v", status)
	}
	if status["suggested_port"] != float64(7402) {
		t.Fatalf("no port was suggested for the first run: %v", status)
	}

	body := fmt.Sprintf(`{"url":%q,"public_url":"http://127.0.0.1:%d","parent_origin":"http://moa.test"}`, upstream.URL, port)
	activated := httptest.NewRecorder()
	handler(activated, httptest.NewRequest("PUT", "/api/preview/target", strings.NewReader(body)))
	if activated.Code != http.StatusOK {
		t.Fatalf("activation = %d %s", activated.Code, activated.Body)
	}
	var result map[string]any
	if err := json.Unmarshal(activated.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["enabled"] != true || !strings.Contains(result["preview_url"].(string), "preview_token=") {
		t.Fatalf("activation response = %v", result)
	}
	if !listening(port) {
		t.Fatal("the API did not bind the listener")
	}

	off := httptest.NewRecorder()
	handler(off, httptest.NewRequest("PUT", "/api/preview/target", strings.NewReader(`{"enabled":false}`)))
	if off.Code != http.StatusOK {
		t.Fatalf("deactivation = %d %s", off.Code, off.Body)
	}
	if listening(port) {
		t.Fatal("the API left the listener open")
	}
}

// An unreachable dev server must not leave the port open: the user asked for a
// preview, not for a listener.
func TestPreviewTargetAPIClosesTheListenerWhenTheTargetIsRefused(t *testing.T) {
	port := freePort(t)
	c := NewPreviewController(7401, nil, &memoryPreviewStore{})
	t.Cleanup(c.Close)
	handler := handlePreviewTarget(c)

	body := fmt.Sprintf(`{"url":"http://169.254.169.254/","public_url":"http://127.0.0.1:%d","parent_origin":"http://moa.test"}`, port)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest("PUT", "/api/preview/target", strings.NewReader(body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("a forbidden target = %d", response.Code)
	}
	if listening(port) {
		t.Fatal("a refused target left a listener behind")
	}
}
