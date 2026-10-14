package testenv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/e-aleixandre/moa"

// Every package with tests that can reach pkg/core must install the harness,
// otherwise its tests resolve the developer's real config again. The check is
// static (imports only), so it needs no go command and stays fast.
func TestEveryPackageReachingCoreInstallsTheHarness(t *testing.T) {
	root := moduleRoot(t)
	type pkg struct {
		prodImports, testImports []string
		hasTests, usesHarness    bool
	}
	pkgs := map[string]*pkg{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		dir, _ := filepath.Rel(root, filepath.Dir(path))
		importPath := modulePath
		if dir != "." {
			importPath += "/" + filepath.ToSlash(dir)
		}
		mode := parser.ImportsOnly
		if strings.HasSuffix(path, "_test.go") {
			mode = parser.SkipObjectResolution // bodies are needed to find the TestMain call
		}
		f, err := parser.ParseFile(fset, path, nil, mode)
		if err != nil {
			return err
		}
		p := pkgs[importPath]
		if p == nil {
			p = &pkg{}
			pkgs[importPath] = p
		}
		var imports []string
		for _, imp := range f.Imports {
			s, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(s, modulePath) {
				imports = append(imports, s)
			}
		}
		if strings.HasSuffix(path, "_test.go") {
			p.hasTests = true
			p.testImports = append(p.testImports, imports...)
			if hasHarnessMain(f) {
				p.usesHarness = true
			}
		} else {
			p.prodImports = append(p.prodImports, imports...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	coreReach := map[string]bool{modulePath + "/pkg/core": true}
	for changed := true; changed; {
		changed = false
		for path, p := range pkgs {
			if coreReach[path] {
				continue
			}
			for _, imp := range p.prodImports {
				if coreReach[imp] {
					coreReach[path], changed = true, true
					break
				}
			}
		}
	}
	var missing []string
	for path, p := range pkgs {
		if !p.hasTests || p.usesHarness || path == modulePath+"/internal/testenv" {
			continue
		}
		reaches := coreReach[path]
		for _, imp := range p.testImports {
			reaches = reaches || coreReach[imp]
		}
		if reaches {
			missing = append(missing, strings.TrimPrefix(path, modulePath+"/"))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("packages reach pkg/core but their tests do not call testenv.Run from TestMain: %v", missing)
	}
}

// hasHarnessMain reports whether f declares func TestMain whose body calls Run
// of the testenv package, under whatever name it is imported. Text in
// comments or strings, and calls from other functions, do not count.
func hasHarnessMain(f *ast.File) bool {
	var names []string
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path != modulePath+"/internal/testenv" {
			continue
		}
		name := "testenv"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names = append(names, name)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return !found
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" {
				if id, ok := sel.X.(*ast.Ident); ok {
					for _, name := range names {
						found = found || id.Name == name
					}
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func parseSrc(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestHasHarnessMain(t *testing.T) {
	const imp = `import (
	"os"
	"testing"
	%s "github.com/e-aleixandre/moa/internal/testenv"
)
`
	for _, tc := range []struct {
		name, src string
		want      bool
	}{
		{"plain", fmt.Sprintf(imp, "") + `func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }`, true},
		{"aliased", fmt.Sprintf(imp, "te") + `func TestMain(m *testing.M) { os.Exit(te.Run(m)) }`, true},
		{"ignored harness", fmt.Sprintf(imp, "") + `func ignoredHarness(m *testing.M) { testenv.Run(m) }`, false},
		{"other function and TestMain without call", fmt.Sprintf(imp, "") + `func helper(m *testing.M) { testenv.Run(m) }
func TestMain(m *testing.M) { os.Exit(m.Run()) }`, false},
		{"comment and string", fmt.Sprintf(imp, "") + `// testenv.Run(m)
func TestMain(m *testing.M) { _ = "testenv.Run("; os.Exit(m.Run()) }`, false},
		{"other Run", fmt.Sprintf(imp, "_") + `func TestMain(m *testing.M) { os.Exit(other.Run(m)) }`, false},
		{"alias mismatch", fmt.Sprintf(imp, "te") + `func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }`, false},
	} {
		if got := hasHarnessMain(parseSrc(t, "package p\n"+tc.src)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
