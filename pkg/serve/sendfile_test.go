package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tool"
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

// storedArtifact reads back the persisted reference for a send_file result,
// straight from the session's own sidecar (no in-memory allowlist exists).
func storedArtifact(t *testing.T, sess *ManagedSession, id string) session.Artifact {
	t.Helper()
	a, ok, err := sess.artifactStore.Get(id)
	if err != nil {
		t.Fatalf("read artifact catalog: %v", err)
	}
	if !ok {
		t.Fatalf("artifact %s not persisted", id)
	}
	return a
}

func sendFilePNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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
	if _, ok := data["title"]; ok {
		t.Errorf("title present without being sent: %v", data["title"])
	}

	artifact := storedArtifact(t, sess, data["file_id"].(string))
	// The stored path is the canonical destination (EvalSymlinks applied), so
	// it is what the download handler will reopen on every request.
	wantPath, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Path != wantPath {
		t.Errorf("stored path = %q, want %q", artifact.Path, wantPath)
	}
}

func TestSendFileTool_TitleAndDescription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "informe.md")
	if err := os.WriteFile(filePath, []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("ok")), tmp)
	sess := newSendFileTestSession(t, mgr, tmp)
	res := execSendFile(t, sendFileTool(t, sess), map[string]any{
		"path": filePath, "title": "  Informe final  ", "description": "Resultado solicitado",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	data := lastLineJSON(t, resultText(res))
	if data["title"] != "Informe final" {
		t.Errorf("title = %v, want trimmed Informe final", data["title"])
	}
	if data["description"] != "Resultado solicitado" {
		t.Errorf("description = %v", data["description"])
	}

	artifact := storedArtifact(t, sess, data["file_id"].(string))
	if artifact.Title != "Informe final" || artifact.Description != "Resultado solicitado" {
		t.Errorf("stored artifact = %#v, want title/description persisted", artifact)
	}

	// A bare re-send keeps the caption instead of blanking it.
	again := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	if again["file_id"] != data["file_id"] {
		t.Errorf("re-send changed the artifact ID: %v -> %v", data["file_id"], again["file_id"])
	}
	kept := storedArtifact(t, sess, data["file_id"].(string))
	if kept.Title != "Informe final" || kept.Description != "Resultado solicitado" {
		t.Errorf("re-send erased optional metadata: %#v", kept)
	}
}

func TestSendFileTool_RejectsNonStringTitle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "a.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("ok")), tmp)
	sess := newSendFileTestSession(t, mgr, tmp)

	res := execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath, "title": 42})
	if !res.IsError {
		t.Fatal("expected error for non-string title")
	}
	res = execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath, "description": "bad\x00nul"})
	if !res.IsError {
		t.Fatal("expected error for NUL in description")
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
	artifact := storedArtifact(t, sess, data["file_id"].(string))
	wantPath, err := core.CanonicalizePath(filepath.Join(tmp, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Path != wantPath {
		t.Errorf("stored path = %q, want resolved against CWD (%q)", artifact.Path, wantPath)
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
	artifacts, err := sess.artifactStore.List()
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("failed send published an artifact: %#v (err %v)", artifacts, err)
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
	if artifact := storedArtifact(t, sess, data["file_id"].(string)); artifact.Name != "Report.csv" {
		t.Errorf("stored name = %q, want Report.csv", artifact.Name)
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
	store := session.NewArtifactStore(t.TempDir(), "sess")
	res := execSendFile(t, newSendFileTool(restricted, "sess", store), map[string]any{"path": outsideFile})
	if !res.IsError {
		t.Fatal("expected error for path outside workspace under restricted PathPolicy")
	}

	unrestricted := tool.ToolConfig{
		WorkspaceRoot: workspace,
		PathPolicy:    tool.NewPathPolicy(workspace, nil, true),
	}
	res = execSendFile(t, newSendFileTool(unrestricted, "sess", session.NewArtifactStore(t.TempDir(), "sess")), map[string]any{"path": outsideFile})
	if res.IsError {
		t.Fatalf("unexpected error in unrestricted mode: %s", resultText(res))
	}
}

// TestSendFileTool_SymlinkOutsidePolicyRejected covers the second SafePath
// check: an allowed symlink inside the workspace whose destination is outside
// it must not publish the outside file.
func TestSendFileTool_SymlinkOutsidePolicyRejected(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("shh"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	cfg := tool.ToolConfig{WorkspaceRoot: workspace, PathPolicy: tool.NewPathPolicy(workspace, nil, false)}
	res := execSendFile(t, newSendFileTool(cfg, "sess", session.NewArtifactStore(t.TempDir(), "sess")), map[string]any{"path": link})
	if !res.IsError {
		t.Fatalf("symlink to an outside file was published: %s", resultText(res))
	}
}

// TestSendFileTool_MetadataWriteFailureFails pins that a publication that
// cannot be persisted is reported as an error: no card may promise a reference
// that does not exist on disk.
func TestSendFileTool_MetadataWriteFailureFails(t *testing.T) {
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "report.txt")
	if err := os.WriteFile(filePath, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sidecar directory that is not writable makes the atomic write fail.
	sidecarDir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(sidecarDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sidecarDir, 0700) })
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores directory permissions")
	}

	cfg := tool.ToolConfig{WorkspaceRoot: workspace, PathPolicy: tool.NewPathPolicy(workspace, nil, true)}
	store := session.NewArtifactStore(sidecarDir, "aaaaaaaaaaaaaaaaaaaaaaaa")
	res := execSendFile(t, newSendFileTool(cfg, "sess", store), map[string]any{"path": filePath})
	if !res.IsError {
		t.Fatalf("send_file succeeded despite an unwritable catalog: %s", resultText(res))
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(content) {
		t.Errorf("body = %q, want %q", body, content)
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
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "sandbox" {
		t.Errorf("Content-Security-Policy = %q, want sandbox", csp)
	}
	if cache := resp.Header.Get("Cache-Control"); cache != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", cache)
	}
	if corp := resp.Header.Get("Cross-Origin-Resource-Policy"); corp != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", corp)
	}
	if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}
	if got, want := resp.Header.Get("Content-Length"), strconv.Itoa(len(content)); got != want {
		t.Errorf("Content-Length = %q, want %q", got, want)
	}
}

// TestDownloadFile_HEADAndRange pins that the descriptor is streamed through
// ServeContent: HEAD reports the size without a body and a Range request gets
// the requested slice.
func TestDownloadFile_HEADAndRange(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	content := []byte("0123456789abcdef")
	filePath := filepath.Join(tmp, "data.bin")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newSendFileTestSession(t, mgr, tmp)
	data := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	url := srv.URL + data["url"].(string)

	headResp, err := http.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	defer headResp.Body.Close() //nolint:errcheck
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD = %d, want 200", headResp.StatusCode)
	}
	if got, want := headResp.Header.Get("Content-Length"), strconv.Itoa(len(content)); got != want {
		t.Errorf("HEAD Content-Length = %q, want %q", got, want)
	}
	headBody, _ := io.ReadAll(headResp.Body)
	if len(headBody) != 0 {
		t.Errorf("HEAD returned a body of %d bytes", len(headBody))
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=4-7")
	rangeResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer rangeResp.Body.Close() //nolint:errcheck
	if rangeResp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range GET = %d, want 206", rangeResp.StatusCode)
	}
	part, _ := io.ReadAll(rangeResp.Body)
	if string(part) != "4567" {
		t.Errorf("range body = %q, want 4567", part)
	}
}

// TestDownloadFile_LargeFileStreamed shows the handler does not snapshot the
// file into memory: a file well past the 32 MiB upload limit is served whole.
func TestDownloadFile_LargeFileStreamed(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "big.bin")
	const size = 40 << 20
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	data := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	if int64(data["size"].(float64)) != size {
		t.Fatalf("reported size = %v, want %d", data["size"], size)
	}
	resp, err := http.Get(srv.URL + data["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET large file = %d, want 200", resp.StatusCode)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil || n != size {
		t.Fatalf("streamed %d bytes (err %v), want %d", n, err, size)
	}
}

// TestDownloadFile_CurrentContentAfterAtomicRename is the product promise: an
// artifact is a live reference, so an atomic replacement at the same path is
// what the next read returns.
func TestDownloadFile_CurrentContentAfterAtomicRename(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "report.txt")
	if err := os.WriteFile(filePath, []byte("first version"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newSendFileTestSession(t, mgr, tmp)
	data := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	url := srv.URL + data["url"].(string)

	staged := filepath.Join(tmp, "report.new")
	if err := os.WriteFile(staged, []byte("second version"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, filePath); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "second version" {
		t.Fatalf("GET after atomic rename = %d, %q; want 200, second version", resp.StatusCode, body)
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

// TestDownloadFile_SourceGone_410 distinguishes a known reference whose source
// disappeared (410, recoverable in the UI) from an unknown artifact (404).
func TestDownloadFile_SourceGone_410(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "ephemeral.txt")
	if err := os.WriteFile(filePath, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	data := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + data["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("GET after source deletion = %d, want 410", resp.StatusCode)
	}
	if !strings.Contains(string(body), "artifact source is unavailable") {
		t.Errorf("410 body = %q, want artifact source is unavailable", body)
	}
	if strings.Contains(string(body), tmp) {
		t.Errorf("410 body leaks the source path: %q", body)
	}
}

func TestSendFileTool_SpoofedImageExtensionServedAsAttachment(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-png.png")
	if err := os.WriteFile(filePath, []byte("this is not image data"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	data := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))

	resp, err := http.Get(srv.URL + data["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET spoofed image = %d, want 200", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("spoofed image Content-Disposition = %q, want attachment", resp.Header.Get("Content-Disposition"))
	}
}

func TestDownloadFile_RealImageServedInline(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	imagePath := filepath.Join(tmp, "preview.png")
	if err := os.WriteFile(imagePath, sendFilePNG(t), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	data := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": imagePath})))
	resp, err := http.Get(srv.URL + data["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline;") {
		t.Fatalf("image headers: Content-Type=%q Content-Disposition=%q", resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
	}
}

// TestDownloadFile_DoesNotShadowFilesListing ensures the /files/{fileID} route
// doesn't eclipse the existing filesystem autocomplete /files listing route.
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
