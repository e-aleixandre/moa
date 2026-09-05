package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/session"
)

// artifactsDTO mirrors the exact wire contract the UI depends on.
type artifactsResponse struct {
	Artifacts []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Mime        string `json:"mime"`
		Size        int64  `json:"size"`
		URL         string `json:"url"`
		CreatedAt   string `json:"created_at"`
		UpdatedAt   string `json:"updated_at"`
		Available   bool   `json:"available"`
	} `json:"artifacts"`
}

func getArtifacts(t *testing.T, srv interface{ url() string }, id string) (artifactsResponse, *http.Response) {
	t.Helper()
	resp, err := http.Get(srv.url() + "/api/sessions/" + id + "/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var out artifactsResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode artifacts body %q: %v", body, err)
		}
	}
	return out, resp
}

type testSrvURL struct{ u string }

func (s testSrvURL) url() string { return s.u }

func TestArtifactsEndpoint_ListsPublishedFiles(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "informe.md")
	if err := os.WriteFile(filePath, []byte("# hola"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	sent := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{
		"path": filePath, "title": "Informe final", "description": "Resultado solicitado",
	})))

	body, resp := getArtifacts(t, testSrvURL{srv.URL}, sess.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /artifacts = %d, want 200", resp.StatusCode)
	}
	if len(body.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v, want 1 entry", body.Artifacts)
	}
	a := body.Artifacts[0]
	if a.ID != sent["file_id"] || a.Name != "informe.md" || a.Title != "Informe final" || a.Description != "Resultado solicitado" {
		t.Errorf("artifact DTO = %#v", a)
	}
	if a.URL != "/api/sessions/"+sess.ID+"/files/"+a.ID {
		t.Errorf("url = %q, want the /files/<id> shape", a.URL)
	}
	if !a.Available || a.Size != int64(len("# hola")) || !strings.HasPrefix(a.Mime, "text/markdown") {
		t.Errorf("artifact liveness = %#v", a)
	}
	if a.CreatedAt == "" || a.UpdatedAt == "" {
		t.Errorf("timestamps missing: %#v", a)
	}

	// A plain file write publishes nothing.
	if err := os.WriteFile(filepath.Join(tmp, "untouched.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ = getArtifacts(t, testSrvURL{srv.URL}, sess.ID)
	if len(body.Artifacts) != 1 {
		t.Fatalf("a bare file write changed the collection: %#v", body.Artifacts)
	}
}

// The empty collection must serialize as [], never null: the UI renders it
// directly.
func TestArtifactsEndpoint_EmptyIsArrayNotNull(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess := newSendFileTestSession(t, mgr, t.TempDir())

	resp, err := http.Get(srv.URL + "/api/sessions/" + sess.ID + "/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), `"artifacts":[]`) {
		t.Fatalf("empty collection body = %s, want an empty array", raw)
	}
}

func TestArtifactsEndpoint_OrderedByUpdatedDescending(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sess := newSendFileTestSession(t, mgr, tmp)
	first := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filepath.Join(tmp, "a.txt")})))
	second := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filepath.Join(tmp, "b.txt")})))

	body, _ := getArtifacts(t, testSrvURL{srv.URL}, sess.ID)
	if len(body.Artifacts) != 2 || body.Artifacts[0].ID != second["file_id"] || body.Artifacts[1].ID != first["file_id"] {
		t.Fatalf("order = %#v, want newest update first", body.Artifacts)
	}
}

// A reference whose source vanished stays in the collection with its last
// known metadata and available:false — the rest of the list still works.
func TestArtifactsEndpoint_MissingSourceIsUnavailableNotFatal(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	gone := filepath.Join(tmp, "gone.txt")
	kept := filepath.Join(tmp, "kept.txt")
	for path, content := range map[string]string{gone: "temporary", kept: "still here"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sess := newSendFileTestSession(t, mgr, tmp)
	execSendFile(t, sendFileTool(t, sess), map[string]any{"path": gone})
	execSendFile(t, sendFileTool(t, sess), map[string]any{"path": kept})
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	body, resp := getArtifacts(t, testSrvURL{srv.URL}, sess.ID)
	if resp.StatusCode != http.StatusOK || len(body.Artifacts) != 2 {
		t.Fatalf("list = %d, %#v; want 200 with both entries", resp.StatusCode, body.Artifacts)
	}
	for _, a := range body.Artifacts {
		switch a.Name {
		case "gone.txt":
			if a.Available {
				t.Errorf("removed source still reported available: %#v", a)
			}
			if a.Size != int64(len("temporary")) {
				t.Errorf("unavailable artifact lost its last known size: %#v", a)
			}
		case "kept.txt":
			if !a.Available {
				t.Errorf("present source reported unavailable: %#v", a)
			}
		}
	}
}

// Listing never leaks the filesystem path.
func TestArtifactsEndpoint_DoesNotExposePath(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "secret-name.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newSendFileTestSession(t, mgr, tmp)
	execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})

	resp, err := http.Get(srv.URL + "/api/sessions/" + sess.ID + "/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), tmp) {
		t.Fatalf("artifact list leaked the source directory: %s", raw)
	}
}

func TestArtifactsEndpoint_UnknownSession404(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()
	resp, err := http.Get(srv.URL + "/api/sessions/aaaaaaaaaaaaaaaaaaaaaaaa/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session = %d, want 404", resp.StatusCode)
	}
}

// A sidecar left behind by a crash, with no session JSON next to it, is not a
// conversation: it must never be served.
func TestArtifactsEndpoint_OrphanSidecarNotServed(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	dir := filepath.Join(mgr.sessionBaseDir, "orphan")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	orphanID := "aaaaaaaaaaaaaaaaaaaaaaaa"
	store := session.NewArtifactStore(dir, orphanID)
	if _, err := store.Upsert("/etc/hostname", session.ArtifactMeta{Name: "hostname"}); err != nil {
		t.Fatal(err)
	}

	_, resp := getArtifacts(t, testSrvURL{srv.URL}, orphanID)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("orphan sidecar list = %d, want 404", resp.StatusCode)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatal("test premise: the orphan catalog should exist on disk")
	}
	dl, err := http.Get(srv.URL + "/api/sessions/" + orphanID + "/files/" + list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close() //nolint:errcheck
	if dl.StatusCode != http.StatusNotFound {
		t.Fatalf("orphan sidecar download = %d, want 404", dl.StatusCode)
	}
}

// A session JSON whose header does not identify it is corrupt, not absent.
func TestArtifactsEndpoint_CorruptHeader500(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	dir := filepath.Join(mgr.sessionBaseDir, "broken")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id := "bbbbbbbbbbbbbbbbbbbbbbbb"
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	_, resp := getArtifacts(t, testSrvURL{srv.URL}, id)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("corrupt header list = %d, want 500", resp.StatusCode)
	}
}

// Reading a saved conversation must not need an LLM run or a resume: after
// close (and after a whole new manager over the same directory) the list and
// the content still answer.
func TestArtifactsEndpoint_SavedAndRestartedSessionWithoutResume(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "informe.md")
	if err := os.WriteFile(filePath, []byte("# durable"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newSendFileTestSession(t, mgr, tmp)
	sessionID := sess.ID
	sent := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))

	if err := mgr.CloseSession(sessionID); err != nil {
		t.Fatal(err)
	}
	if _, active := mgr.Get(sessionID); active {
		t.Fatal("session still active after close (test premise)")
	}

	body, resp := getArtifacts(t, testSrvURL{srv.URL}, sessionID)
	if resp.StatusCode != http.StatusOK || len(body.Artifacts) != 1 || body.Artifacts[0].ID != sent["file_id"] {
		t.Fatalf("unloaded session list = %d, %#v", resp.StatusCode, body.Artifacts)
	}
	dl, err := http.Get(srv.URL + sent["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close() //nolint:errcheck
	content, _ := io.ReadAll(dl.Body)
	if dl.StatusCode != http.StatusOK || string(content) != "# durable" {
		t.Fatalf("unloaded session download = %d, %q", dl.StatusCode, content)
	}
	if _, active := mgr.Get(sessionID); active {
		t.Fatal("reading artifacts resumed the session")
	}
}

// Different conversations that published the SAME path get distinct IDs and
// neither can read the other's reference.
func TestArtifactsEndpoint_SessionIsolation(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "shared.txt")
	if err := os.WriteFile(filePath, []byte("shared"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessA := newSendFileTestSession(t, mgr, tmp)
	sessB := newSendFileTestSession(t, mgr, tmp)
	a := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sessA), map[string]any{"path": filePath})))
	b := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sessB), map[string]any{"path": filePath})))
	if a["file_id"] == b["file_id"] {
		t.Fatal("the same path shares an artifact ID across conversations")
	}

	resp, err := http.Get(srv.URL + "/api/sessions/" + sessB.ID + "/files/" + a["file_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-session read = %d, want 404", resp.StatusCode)
	}
	listB, _ := getArtifacts(t, testSrvURL{srv.URL}, sessB.ID)
	if len(listB.Artifacts) != 1 || listB.Artifacts[0].ID != b["file_id"] {
		t.Fatalf("session B list = %#v, want only its own reference", listB.Artifacts)
	}
}

// Deleting a conversation removes its references and nothing else: the file
// stays on disk and another conversation's reference to it keeps working.
func TestArtifactsEndpoint_DeleteActiveSessionRemovesOnlyReferences(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "shared.txt")
	if err := os.WriteFile(filePath, []byte("original bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessA := newSendFileTestSession(t, mgr, tmp)
	sessB := newSendFileTestSession(t, mgr, tmp)
	a := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sessA), map[string]any{"path": filePath})))
	b := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sessB), map[string]any{"path": filePath})))
	sidecar := sessA.artifactStore.Path()

	if err := mgr.Delete(sessA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("artifact sidecar survived the delete: %v", err)
	}
	if data, err := os.ReadFile(filePath); err != nil || string(data) != "original bytes" {
		t.Fatalf("delete touched the original file: %q, %v", data, err)
	}

	gone, err := http.Get(srv.URL + a["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer gone.Body.Close() //nolint:errcheck
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted session's artifact = %d, want 404", gone.StatusCode)
	}
	alive, err := http.Get(srv.URL + b["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer alive.Body.Close() //nolint:errcheck
	if alive.StatusCode != http.StatusOK {
		t.Fatalf("other conversation's reference = %d, want 200", alive.StatusCode)
	}
}

func TestArtifactsEndpoint_DeleteUnloadedSessionRemovesOnlyReferences(t *testing.T) {
	testDeleteUnloadedArtifactSession(t, false)
}

func TestArtifactsEndpoint_DeleteUnloadedCorruptSessionRemovesOnlyReferences(t *testing.T) {
	testDeleteUnloadedArtifactSession(t, true)
}

func testDeleteUnloadedArtifactSession(t *testing.T, corruptHeader bool) {
	t.Helper()
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "report.txt")
	if err := os.WriteFile(filePath, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newSendFileTestSession(t, mgr, tmp)
	sessionID := sess.ID
	sent := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	sidecar := sess.artifactStore.Path()
	if err := mgr.CloseSession(sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("close removed the artifact sidecar: %v", err)
	}
	jsonPath := filepath.Join(filepath.Dir(sidecar), sessionID+".json")
	if corruptHeader {
		if err := os.WriteFile(jsonPath, []byte("{corrupt"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := mgr.Delete(sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Fatalf("unloaded delete left the session JSON: %v", err)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("unloaded delete left the sidecar: %v", err)
	}
	if data, err := os.ReadFile(filePath); err != nil || string(data) != "keep me" {
		t.Fatalf("unloaded delete touched the original file: %q, %v", data, err)
	}
	resp, err := http.Get(srv.URL + sent["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted unloaded artifact = %d, want 404", resp.StatusCode)
	}
}

// Close is not delete: the references survive and come back on resume, with
// the same IDs and URLs.
func TestArtifactsEndpoint_ResumePreservesReferences(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "report.txt")
	if err := os.WriteFile(filePath, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newSendFileTestSession(t, mgr, tmp)
	sessionID := sess.ID
	sent := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	if err := mgr.CloseSession(sessionID); err != nil {
		t.Fatal(err)
	}

	resumed, err := mgr.ResumeSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.artifactStore == nil {
		t.Fatal("resumed session has no artifact store")
	}
	body, resp := getArtifacts(t, testSrvURL{srv.URL}, sessionID)
	if resp.StatusCode != http.StatusOK || len(body.Artifacts) != 1 || body.Artifacts[0].ID != sent["file_id"] {
		t.Fatalf("resumed list = %d, %#v", resp.StatusCode, body.Artifacts)
	}
	// A re-send after resume must update the same reference, not create a
	// second one — the store has to be the one next to the session JSON.
	again := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, resumed), map[string]any{"path": filePath})))
	if again["file_id"] != sent["file_id"] {
		t.Errorf("re-send after resume created a new ID: %v -> %v", sent["file_id"], again["file_id"])
	}
	body, _ = getArtifacts(t, testSrvURL{srv.URL}, sessionID)
	if len(body.Artifacts) != 1 {
		t.Fatalf("re-send after resume duplicated the entry: %#v", body.Artifacts)
	}
}
