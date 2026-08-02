// Package docsdrift holds parsing helpers for the documentation drift tests.
//
// The docs under docs/*.md are hand-written prose and stay that way: nothing
// here generates markdown. These helpers only extract the *keys* a table
// documents (tool names, flag names, config keys, …) so a test can compare
// them against the keys the code actually exposes. Descriptions are never
// compared — only membership.
//
// Several of the code-side key sets live in unexported identifiers (the CLI
// flag set is built inline in main(), modelAliases and allCommands are package
// private). Rather than exporting them just to be testable, the helpers below
// read the source with go/ast. That keeps production code untouched and the
// coupling confined to this package.
package docsdrift

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RepoRoot walks up from the working directory until it finds go.mod, so the
// tests work from any package directory without absolute paths.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("docsdrift: go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// ReadDoc reads a documentation file relative to the repository root.
func ReadDoc(root, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// TableAfter returns the data rows of the first markdown table that starts
// after the line equal to anchor (a heading, or a lead-in line such as
// "Always registered:"). Header and separator rows are dropped.
func TableAfter(content, anchor string) ([][]string, error) {
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == anchor {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("anchor %q not found", anchor)
	}
	for i := start; i < len(lines); i++ {
		if !isTableLine(lines[i]) {
			continue
		}
		end := i
		for end < len(lines) && isTableLine(lines[end]) {
			end++
		}
		return tableBlockRows(lines[i:end]), nil
	}
	return nil, fmt.Errorf("no markdown table after anchor %q", anchor)
}

// TablesInSection returns the data rows of every markdown table between the
// given heading and the next heading of the same or higher level. Used for
// sections whose keys are spread over several sub-tables (config fields).
func TablesInSection(content, heading string) ([][]string, error) {
	level := headingLevel(heading)
	if level == 0 {
		return nil, fmt.Errorf("%q is not a markdown heading", heading)
	}
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("heading %q not found", heading)
	}
	var rows [][]string
	for i := start; i < len(lines); i++ {
		if l := headingLevel(strings.TrimSpace(lines[i])); l > 0 && l <= level {
			break
		}
		if !isTableLine(lines[i]) {
			continue
		}
		end := i
		for end < len(lines) && isTableLine(lines[end]) {
			end++
		}
		rows = append(rows, tableBlockRows(lines[i:end])...)
		i = end
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no markdown tables in section %q", heading)
	}
	return rows, nil
}

func headingLevel(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n >= len(line) || line[n] != ' ' {
		return 0
	}
	return n
}

func isTableLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "|")
}

func tableBlockRows(block []string) [][]string {
	var rows [][]string
	for i, line := range block {
		cells := SplitTableRow(line)
		if i == 0 || isSeparatorRow(cells) {
			continue // header / |---|---| divider
		}
		rows = append(rows, cells)
	}
	return rows
}

func isSeparatorRow(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return len(cells) > 0
}

// SplitTableRow splits a markdown table row into trimmed cells. Pipes escaped
// as `\|` (used by cells like `/path [list\|add\|rm]`) are part of the cell
// text, not separators.
func SplitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")

	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

// BacktickToken returns the text inside the first pair of backticks in cell,
// or "" when the cell has no code span.
func BacktickToken(cell string) string {
	start := strings.Index(cell, "`")
	if start < 0 {
		return ""
	}
	rest := cell[start+1:]
	end := strings.Index(rest, "`")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// AllBacktickTokens returns the text inside every pair of backticks in cell.
// Used where a token may appear anywhere in the row (an alias mentioned in a
// description, e.g. "Quit (alias `/quit`)"), not only as the row's key.
func AllBacktickTokens(cell string) []string {
	var out []string
	for {
		start := strings.Index(cell, "`")
		if start < 0 {
			return out
		}
		rest := cell[start+1:]
		end := strings.Index(rest, "`")
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		cell = rest[end+1:]
	}
}

// flagNameArg maps every flag-registering method of flag/flag.FlagSet to the
// index of its flag-name argument. The plain forms (String, Bool, …) take the
// name first; the *Var forms take the destination pointer first, so their name
// is the second argument. Var and TextVar follow the *Var shape.
var flagNameArg = map[string]int{
	"Bool": 0, "BoolFunc": 0, "Duration": 0, "Float64": 0, "Func": 0,
	"Int": 0, "Int64": 0, "String": 0, "Uint": 0, "Uint64": 0,

	"BoolVar": 1, "DurationVar": 1, "Float64Var": 1, "Int64Var": 1,
	"IntVar": 1, "StringVar": 1, "TextVar": 1, "Uint64Var": 1,
	"UintVar": 1, "Var": 1,
}

// nonRegisteringFlagMethods lists the methods that hang off the same receiver
// but declare no flag: parsing, querying and output plumbing. They are
// enumerated explicitly so that GoFlagNames can treat *anything else* as a
// method it does not understand and fail, instead of silently skipping it. A
// new flag registered through an unlisted method (or with a computed name)
// would otherwise slip past the docs-drift test as a false green.
var nonRegisteringFlagMethods = map[string]bool{
	"Arg": true, "Args": true, "ErrorHandling": true, "Init": true,
	"Lookup": true, "NArg": true, "NFlag": true, "Name": true,
	"NewFlagSet": true, "Output": true, "Parse": true, "Parsed": true,
	"PrintDefaults": true, "Set": true, "SetOutput": true, "Usage": true,
	"Visit": true, "VisitAll": true,
}

// GoFlagNames extracts flag names declared in a Go file through calls like
// flag.String("p", …), fs.Bool("check", …) or flag.StringVar(&x, "p", …).
// receiver is the identifier the calls hang off ("flag" for the default flag
// set, "fs" for a sub-command's FlagSet).
//
// Any call on that receiver whose method is neither a known registrar nor a
// known non-registrar is an error: an extractor that no longer understands the
// code must say so rather than report a green test over a partial view of it.
func GoFlagNames(path, receiver string) ([]string, error) {
	file, fset, err := parseGoFile(path)
	if err != nil {
		return nil, err
	}
	var names []string
	var bad error
	ast.Inspect(file, func(n ast.Node) bool {
		if bad != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != receiver {
			return true
		}
		method := sel.Sel.Name
		idx, ok := flagNameArg[method]
		if !ok {
			if !nonRegisteringFlagMethods[method] {
				bad = fmt.Errorf("%s: unknown call %s.%s(…): the flag extractor cannot tell whether it registers a flag; "+
					"add it to flagNameArg (with its name-argument index) or to nonRegisteringFlagMethods",
					fset.Position(call.Pos()), receiver, method)
			}
			return true
		}
		if len(call.Args) <= idx {
			bad = fmt.Errorf("%s: %s.%s(…) has %d arguments, expected the flag name at index %d",
				fset.Position(call.Pos()), receiver, method, len(call.Args), idx)
			return true
		}
		s, ok := stringLit(call.Args[idx])
		if !ok {
			bad = fmt.Errorf("%s: %s.%s(…) declares a flag whose name is not a string literal; "+
				"the docs-drift test cannot check it", fset.Position(call.Pos()), receiver, method)
			return true
		}
		names = append(names, s)
		return true
	})
	if bad != nil {
		return nil, bad
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no %s.* flag declarations found in %s", receiver, path)
	}
	return names, nil
}

// GoDispatchedCommandNames extracts the command names a dispatcher function
// accepts: the string literals it compares the command variable against. It
// recognises the three shapes used by pkg/tui's handleCommand —
//
//	switch cmd { case "branch", "back": … }
//	cmd == "prompt" / strings.HasPrefix(cmd, "prompt ")
//	cutCommand(cmd, "compact")
//
// — which is where aliases actually live: allCommands lists one canonical name
// per action, so an alias added to the dispatcher alone would never show up in
// a palette-only comparison.
//
// Known limitation: only comparisons against a literal, inside the named
// function, and against the named variable are seen. A command routed through
// a helper, a table or a computed name is invisible here; the test would then
// under-report rather than mis-report.
func GoDispatchedCommandNames(path, funcName, cmdVar string) ([]string, error) {
	file, _, err := parseGoFile(path)
	if err != nil {
		return nil, err
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == funcName && fn.Body != nil {
			body = fn.Body
			break
		}
	}
	if body == nil {
		return nil, fmt.Errorf("func %s not found in %s", funcName, path)
	}

	isCmdVar := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == cmdVar
	}
	var names []string
	add := func(e ast.Expr) {
		if s, ok := stringLit(e); ok {
			// "prompt " (prefix match with an argument) and "prompt" name the
			// same command.
			names = append(names, strings.TrimSpace(s))
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SwitchStmt:
			if !isCmdVar(node.Tag) {
				return true
			}
			for _, stmt := range node.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					add(expr)
				}
			}
		case *ast.BinaryExpr:
			if node.Op == token.EQL && isCmdVar(node.X) {
				add(node.Y)
			}
		case *ast.CallExpr:
			// strings.HasPrefix(cmd, "x ") and cutCommand(cmd, "x").
			if len(node.Args) == 2 && isCmdVar(node.Args[0]) {
				add(node.Args[1])
			}
		}
		return true
	})
	if len(names) == 0 {
		return nil, fmt.Errorf("no %s comparisons found in %s.%s", cmdVar, path, funcName)
	}
	return names, nil
}

// GoStringMap extracts a package-level `var name = map[string]string{…}`
// literal with constant keys and values. An entry that is not a
// literal-to-literal pair is an error rather than a skip: silently dropping it
// would hide exactly the entry the docs may be missing.
func GoStringMap(path, name string) (map[string]string, error) {
	lit, fset, err := varCompositeLit(path, name)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(lit.Elts))
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil, fmt.Errorf("%s: entry of map %s is not a key/value pair; the docs-drift test cannot read it",
				fset.Position(elt.Pos()), name)
		}
		k, kok := stringLit(kv.Key)
		if !kok {
			return nil, fmt.Errorf("%s: key of map %s is not a string literal; the docs-drift test cannot read it",
				fset.Position(kv.Key.Pos()), name)
		}
		v, vok := stringLit(kv.Value)
		if !vok {
			return nil, fmt.Errorf("%s: value of map entry %s[%q] is not a string literal; the docs-drift test cannot read it",
				fset.Position(kv.Value.Pos()), name, k)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("map %s in %s has no constant entries", name, path)
	}
	return out, nil
}

// GoStructSliceField extracts one string field from every element of a
// package-level `var name = []T{{Field: "…"}, …}` slice literal. Every element
// must carry the field as a string literal: an element the extractor cannot
// read is an element whose documentation cannot be checked, so it fails loudly
// instead of shrinking the set under comparison.
func GoStructSliceField(path, name, field string) ([]string, error) {
	lit, fset, err := varCompositeLit(path, name)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, elt := range lit.Elts {
		el, ok := elt.(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("%s: element of slice %s is not a struct literal; the docs-drift test cannot read it",
				fset.Position(elt.Pos()), name)
		}
		found := false
		for _, f := range el.Elts {
			kv, ok := f.(*ast.KeyValueExpr)
			if !ok {
				return nil, fmt.Errorf("%s: element of slice %s uses positional fields; the docs-drift test needs %s: \"…\"",
					fset.Position(f.Pos()), name, field)
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != field {
				continue
			}
			s, ok := stringLit(kv.Value)
			if !ok {
				return nil, fmt.Errorf("%s: field %s of a %s element is not a string literal; the docs-drift test cannot read it",
					fset.Position(kv.Value.Pos()), field, name)
			}
			out = append(out, s)
			found = true
		}
		if !found {
			return nil, fmt.Errorf("%s: element of slice %s has no %s field; the docs-drift test cannot identify it",
				fset.Position(elt.Pos()), name, field)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("slice %s in %s has no %s values", name, path, field)
	}
	return out, nil
}

func varCompositeLit(path, name string) (*ast.CompositeLit, *token.FileSet, error) {
	file, fset, err := parseGoFile(path)
	if err != nil {
		return nil, nil, err
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range vs.Names {
				if ident.Name != name || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					return nil, nil, fmt.Errorf("var %s in %s is not a composite literal", name, path)
				}
				return lit, fset, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("var %s not found in %s", name, path)
}

// parseGoFile returns the AST and its FileSet, which callers need to report
// file:line positions in the "I don't understand this code" errors.
func parseGoFile(path string) (*ast.File, *token.FileSet, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	return file, fset, err
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// Diff reports which entries of want are absent from got and vice versa.
func Diff(want, got map[string]bool) (missing, extra []string) {
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	return missing, extra
}

// SetOf builds a membership set from a slice.
func SetOf(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, s := range items {
		set[s] = true
	}
	return set
}
