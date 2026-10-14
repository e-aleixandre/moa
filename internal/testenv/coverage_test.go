package testenv

import (
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
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
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
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), "testenv.Run(") {
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
