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
	res := execSendFile(t, newSendFileTool(restricted, "sess", newSharedFiles(), nil), map[string]any{"path": outsideFile})
	if !res.IsError {
		t.Fatal("expected error for path outside workspace under restricted PathPolicy")
	}

	unrestricted := tool.ToolConfig{
		WorkspaceRoot: workspace,
		PathPolicy:    tool.NewPathPolicy(workspace, nil, true),
	}
	res = execSendFile(t, newSendFileTool(unrestricted, "sess", newSharedFiles(), nil), map[string]any{"path": outsideFile})
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
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "sandbox" {
		t.Errorf("Content-Security-Policy = %q, want sandbox", csp)
	}
	if cache := resp.Header.Get("Cache-Control"); cache != "private, max-age=0, must-revalidate" {
		t.Errorf("Cache-Control = %q, want private, max-age=0, must-revalidate", cache)
	}
	if corp := resp.Header.Get("Cross-Origin-Resource-Policy"); corp != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", corp)
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

func TestDownloadFile_DeletedFromDisk_ServedFromStore(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	content := []byte("durable bytes")
	filePath := filepath.Join(tmp, "ephemeral.txt")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != string(content) {
		t.Fatalf("GET after source deletion = %d, %q; want 200, %q", resp.StatusCode, body, content)
	}
	if got, want := resp.Header.Get("Content-Length"), strconv.FormatInt(int64(len(content)), 10); got != want {
		t.Errorf("Content-Length = %q, want %q", got, want)
	}
}

func TestDownloadFile_DeletedFromDisk_404WithoutStore(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	mgr.attachStore = nil
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "ephemeral.txt")
	if err := os.WriteFile(filePath, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	res := execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})
	data := lastLineJSON(t, resultText(res))
	if f, ok := sess.sharedFiles.get(data["file_id"].(string)); !ok || f.AttachmentID != "" {
		t.Fatal("send_file unexpectedly stored a durable attachment without a store")
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + data["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after source deletion without store = %d, want 404", resp.StatusCode)
	}
}

func TestSendFileTool_PutRefFailureFallsBackToLegacyPath(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "ephemeral.txt")
	if err := os.WriteFile(filePath, []byte("legacy bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	// A dot is rejected by attachment.Store's session ID validation, while
	// remaining safe as a single URL path segment for the download handler.
	invalidSessionID := "invalid.session"
	mgr.mu.Lock()
	delete(mgr.sessions, sess.ID)
	sess.ID = invalidSessionID
	mgr.sessions[invalidSessionID] = sess
	mgr.mu.Unlock()

	cfg := tool.ToolConfig{
		WorkspaceRoot: tmp,
		PathPolicy:    tool.NewPathPolicy(tmp, nil, true),
	}
	res := execSendFile(t, newSendFileTool(cfg, sess.ID, sess.sharedFiles, mgr.attachStore), map[string]any{"path": filePath})
	if res.IsError {
		t.Fatalf("PutRef failure unexpectedly failed send_file: %s", resultText(res))
	}
	data := lastLineJSON(t, resultText(res))
	url, ok := data["url"].(string)
	if !ok || url == "" {
		t.Fatalf("result URL = %#v, want non-empty string", data["url"])
	}
	shared, ok := sess.sharedFiles.get(data["file_id"].(string))
	if !ok || shared.AttachmentID != "" {
		t.Fatalf("shared file = %#v, want legacy entry without AttachmentID", shared)
	}

	resp, err := http.Get(srv.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET with source present = %d, want 200", resp.StatusCode)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(srv.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after source deletion = %d, want 404", resp.StatusCode)
	}
}

func TestDownloadFile_ReleasedDurableAttachmentDoesNotFallBackToPath(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "report.txt")
	if err := os.WriteFile(filePath, []byte("original bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	data := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	shared, ok := sess.sharedFiles.get(data["file_id"].(string))
	if !ok || shared.AttachmentID == "" {
		t.Fatal("send_file did not store a durable attachment")
	}
	if err := mgr.attachStore.RemoveRef(sess.ID, shared.AttachmentID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("replacement bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + data["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after releasing durable attachment = %d, %q; want 404 file unavailable", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "file unavailable") {
		t.Errorf("404 body = %q, want file unavailable", body)
	}
}

func TestSendFileTool_SpoofedImageExtensionStoredAsFile(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-png.png")
	if err := os.WriteFile(filePath, []byte("this is not image data"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	data := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	shared, ok := sess.sharedFiles.get(data["file_id"].(string))
	if !ok || shared.AttachmentID == "" {
		t.Fatal("spoofed image was not stored durably")
	}
	descriptor, ok := mgr.attachStore.Lookup(sess.ID, shared.AttachmentID)
	if !ok || descriptor.Kind != "file" {
		t.Fatalf("spoofed image descriptor = %#v, want kind file", descriptor)
	}

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
	if strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline;") {
		t.Fatalf("spoofed image Content-Disposition = %q, must not be inline", resp.Header.Get("Content-Disposition"))
	}
}

func TestSendFileStoreKindAndSessionDeleteRelease(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	tmp := t.TempDir()
	imagePath := filepath.Join(tmp, "preview.png")
	if err := os.WriteFile(imagePath, sendFilePNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(tmp, "archive.bin")
	if err := os.WriteFile(filePath, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := newSendFileTestSession(t, mgr, tmp)
	imageResult := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": imagePath})))
	imageFile, ok := sess.sharedFiles.get(imageResult["file_id"].(string))
	if !ok || imageFile.AttachmentID == "" {
		t.Fatal("image was not stored durably")
	}
	imageDescriptor, ok := mgr.attachStore.Lookup(sess.ID, imageFile.AttachmentID)
	if !ok || imageDescriptor.Kind != "image" || imageDescriptor.Width == 0 || imageDescriptor.Height == 0 {
		t.Fatalf("image descriptor = %#v, want raster image with dimensions", imageDescriptor)
	}
	imageResp, err := http.Get(srv.URL + imageResult["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	imageResp.Body.Close() //nolint:errcheck
	if imageResp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(imageResp.Header.Get("Content-Disposition"), "inline;") {
		t.Fatalf("image headers: Content-Type=%q Content-Disposition=%q", imageResp.Header.Get("Content-Type"), imageResp.Header.Get("Content-Disposition"))
	}

	fileResult := lastLineJSON(t, resultText(execSendFile(t, sendFileTool(t, sess), map[string]any{"path": filePath})))
	shared, ok := sess.sharedFiles.get(fileResult["file_id"].(string))
	if !ok || shared.AttachmentID == "" {
		t.Fatal("file was not stored durably")
	}
	fileDescriptor, ok := mgr.attachStore.Lookup(sess.ID, shared.AttachmentID)
	if !ok || fileDescriptor.Kind != "file" {
		t.Fatalf("file descriptor = %#v, want kind file", fileDescriptor)
	}
	fileResp, err := http.Get(srv.URL + fileResult["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	fileResp.Body.Close() //nolint:errcheck
	if !strings.HasPrefix(fileResp.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("file Content-Disposition = %q, want attachment", fileResp.Header.Get("Content-Disposition"))
	}

	store := mgr.attachStore
	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Lookup(sess.ID, imageFile.AttachmentID); ok {
		t.Fatal("session delete did not release send_file image")
	}
	if _, _, err := store.Open(sess.ID, shared.AttachmentID); err == nil {
		t.Fatal("session delete left send_file file readable")
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
