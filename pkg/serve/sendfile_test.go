package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
)

// --- test helpers ---

func newSendFileTestSession(t *testing.T, mgr *Manager, cwd string) *ManagedSession {
	t.Helper()
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func sendFileTool(t *testing.T, sess *ManagedSession) core.Tool {
	t.Helper()
	tl, ok := sess.infra.toolReg.Get("send_file")
	if !ok {
		t.Fatal("send_file tool not registered on session")
	}
	return tl
}

func execSendFile(t *testing.T, tool core.Tool, params map[string]any) core.Result {
	t.Helper()
	res, err := tool.Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func resultText(res core.Result) string {
	var sb strings.Builder
	for _, c := range res.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// lastLineJSON parses the last line of a tool result as JSON, the convention
// used by send_file's result (human line + JSON line for FileCard.jsx).
func lastLineJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("parse json line %q: %v", lines[len(lines)-1], err)
	}
	return m
}

// --- tool tests ---

func TestSendFileTool_AbsolutePath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "informe.pdf")
	if err := os.WriteFile(filePath, []byte("hello pdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	prov := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManagerWithRoot(t, ctx, prov, tmp)
	sess := newSendFileTestSession(t, mgr, tmp)
	tool := sendFileTool(t, sess)

	res := execSendFile(t, tool, map[string]any{"path": filePath})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}

	data := lastLineJSON(t, resultText(res))
	if data["name"] != "informe.pdf" {
		t.Errorf("name = %v, want informe.pdf", data["name"])
	}
	if data["mime"] != "application/pdf" {
		t.Errorf("mime = %v, want application/pdf", data["mime"])
	}
	wantURL := "/api/sessions/" + sess.ID + "/files/" + data["file_id"].(string)
	if data["url"] != wantURL {
		t.Errorf("url = %v, want %v", data["url"], wantURL)
	}

	f, ok := sess.sharedFiles.get(data["file_id"].(string))
	if !ok {
		t.Fatal("file not registered in sharedFiles")
	}
	// The test manager runs unrestricted (yolo mode), where SafePath passes an
	// absolute path through filepath.Clean without resolving symlinks — same
	// as the read/write built-ins.
	wantPath := filepath.Clean(filePath)
	if f.Path != wantPath {
		t.Errorf("registered path = %q, want %q", f.Path, wantPath)
	}
}

func TestSendFileTool_RelativePath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	prov := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManagerWithRoot(t, ctx, prov, tmp)
	sess := newSendFileTestSession(t, mgr, tmp)
	tool := sendFileTool(t, sess)

	res := execSendFile(t, tool, map[string]any{"path": "notes.txt"})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	data := lastLineJSON(t, resultText(res))
	f, ok := sess.sharedFiles.get(data["file_id"].(string))
	if !ok {
		t.Fatal("file not registered")
	}
	wantPath, err := core.CanonicalizePath(filepath.Join(tmp, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != wantPath {
		t.Errorf("registered path = %q, want resolved against CWD (%q)", f.Path, wantPath)
	}
}

func TestSendFileTool_NonexistentPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()

	prov := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManagerWithRoot(t, ctx, prov, tmp)
	sess := newSendFileTestSession(t, mgr, tmp)
	tool := sendFileTool(t, sess)

	res := execSendFile(t, tool, map[string]any{"path": filepath.Join(tmp, "missing.txt")})
	if !res.IsError {
		t.Fatal("expected IsError for nonexistent path")
	}
}

func TestSendFileTool_Directory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	prov := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManagerWithRoot(t, ctx, prov, tmp)
	sess := newSendFileTestSession(t, mgr, tmp)
	tool := sendFileTool(t, sess)

	res := execSendFile(t, tool, map[string]any{"path": sub})
	if !res.IsError {
		t.Fatal("expected IsError for a directory")
	}
}

func TestSendFileTool_NameOverride(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "raw-export-123.csv")
	if err := os.WriteFile(filePath, []byte("a,b,c"), 0o644); err != nil {
		t.Fatal(err)
	}

	prov := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManagerWithRoot(t, ctx, prov, tmp)
	sess := newSendFileTestSession(t, mgr, tmp)
	tool := sendFileTool(t, sess)

	res := execSendFile(t, tool, map[string]any{"path": filePath, "name": "some/dir/Report.csv"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	data := lastLineJSON(t, resultText(res))
	if data["name"] != "Report.csv" {
		t.Errorf("name = %v, want Report.csv (basename applied)", data["name"])
	}
	f, ok := sess.sharedFiles.get(data["file_id"].(string))
	if !ok || f.Name != "Report.csv" {
		t.Errorf("registered name = %q, want Report.csv", f.Name)
	}
}

// --- PathPolicy enforcement tests ---

// TestSendFileTool_RespectsPathPolicy pins that send_file resolves paths via
// tool.SafePath, the same boundary the read/write/etc built-ins use: a
// restricted PathPolicy rejects a path outside the workspace, while
// unrestricted (yolo) mode — how the server actually runs — allows it.
func TestSendFileTool_RespectsPathPolicy(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("shh"), 0o644); err != nil {
		t.Fatal(err)
	}

	restricted := tool.ToolConfig{
		WorkspaceRoot: workspace,
		PathPolicy:    tool.NewPathPolicy(workspace, nil, false),
	}
	res := execSendFile(t, newSendFileTool(restricted, "sess", newSharedFiles()), map[string]any{"path": outsideFile})
	if !res.IsError {
		t.Fatal("expected error for path outside workspace under restricted PathPolicy")
	}

	unrestricted := tool.ToolConfig{
		WorkspaceRoot: workspace,
		PathPolicy:    tool.NewPathPolicy(workspace, nil, true),
	}
	res = execSendFile(t, newSendFileTool(unrestricted, "sess", newSharedFiles()), map[string]any{"path": outsideFile})
	if res.IsError {
		t.Fatalf("unexpected error in unrestricted mode: %s", resultText(res))
	}
}

// --- endpoint tests ---

func TestDownloadFile_OK(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	content := []byte("file contents with UTF-8 café")
	filePath := filepath.Join(tmp, "café report.txt")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	tool := sendFileTool(t, sess)
	res := execSendFile(t, tool, map[string]any{"path": filePath})
	data := lastLineJSON(t, resultText(res))
	url := data["url"].(string)

	resp, err := http.Get(srv.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := make([]byte, len(content))
	n, _ := resp.Body.Read(body)
	if string(body[:n]) != string(content) {
		t.Errorf("body = %q, want %q", body[:n], content)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q, want attachment; prefix", cd)
	}
	if !strings.Contains(cd, "filename") {
		t.Errorf("Content-Disposition = %q, missing filename", cd)
	}
}

func TestDownloadFile_UnregisteredFileID_404(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	sess := newSendFileTestSession(t, mgr, tmp)

	resp, err := http.Get(srv.URL + "/api/sessions/" + sess.ID + "/files/deadbeefdeadbeef")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDownloadFile_FileIDFromOtherSession_404(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(filePath, []byte("shh"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessA := newSendFileTestSession(t, mgr, tmp)
	tool := sendFileTool(t, sessA)
	res := execSendFile(t, tool, map[string]any{"path": filePath})
	data := lastLineJSON(t, resultText(res))
	fileID := data["file_id"].(string)

	sessB := newSendFileTestSession(t, mgr, tmp)

	resp, err := http.Get(srv.URL + "/api/sessions/" + sessB.ID + "/files/" + fileID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for fileID from another session, got %d", resp.StatusCode)
	}
}

func TestDownloadFile_UnknownSession_404(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp, err := http.Get(srv.URL + "/api/sessions/nonexistent/files/deadbeefdeadbeef")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDownloadFile_DeletedFromDisk_404(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "ephemeral.txt")
	if err := os.WriteFile(filePath, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	tool := sendFileTool(t, sess)
	res := execSendFile(t, tool, map[string]any{"path": filePath})
	data := lastLineJSON(t, resultText(res))
	url := data["url"].(string)

	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after file deleted from disk, got %d", resp.StatusCode)
	}
}

// TestDownloadFile_DoesNotShadowFilesListing ensures the new
// /files/{fileID} route doesn't eclipse the existing /files listing route.
func TestDownloadFile_DoesNotShadowFilesListing(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	sess := newSendFileTestSession(t, mgr, tmp)

	resp, err := http.Get(srv.URL + "/api/sessions/" + sess.ID + "/files")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from files listing, got %d", resp.StatusCode)
	}
}
