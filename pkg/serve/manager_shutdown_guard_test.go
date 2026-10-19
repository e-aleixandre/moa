package serve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServeTestsShutDownTheirManagers makes every test that builds a Manager
// shut it down. A Manager never shut down keeps its task watcher alive, and
// the watcher can recreate the task database inside a TempDir while the test
// removes it ("directory not empty"). A function that calls NewManager must
// shut it down itself (its body mentions Shutdown) or be listed below with
// where that happens; otherwise it uses startTestManager.
func TestServeTestsShutDownTheirManagers(t *testing.T) {
	shutElsewhere := map[string]string{
		"manager_test.go:startTestManager":                  "registers the cleanup",
		"ask_async_capacity_test.go:f1CapacityEnv":          "f1Env.halt in its cleanup",
		"ask_async_contract_test.go:f1NewEnv":               "f1Env.halt in its cleanup",
		"ask_async_crash_contract_test.go:f1CrashManager":   "crash children exit abruptly; the parent owns the TempDirs",
		"compaction_resume_test.go:newResumeSummaryFixture": "resumeSummaryFixture.stop in its cleanup",
		"task_notices_test.go:newNoticeManagerAt":           "every caller shuts its Manager down",
		"task_scheduler_harness_test.go:start":              "the harness cleanup shuts h.mgr down",
	}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "NewManager" {
					return true
				}
				if _, ok := shutElsewhere[f+":"+fn.Name.Name]; ok || strings.Contains(body, "Shutdown") {
					return true
				}
				t.Errorf("%s: %s builds a Manager it never shuts down; use startTestManager", fset.Position(call.Pos()), fn.Name.Name)
				return true
			})
		}
	}
}
