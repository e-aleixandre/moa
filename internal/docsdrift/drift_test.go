package docsdrift

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/memory"
	"github.com/e-aleixandre/moa/pkg/skill"
	"github.com/e-aleixandre/moa/pkg/subagent"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"github.com/e-aleixandre/moa/pkg/tool"
	"github.com/e-aleixandre/moa/pkg/verify"
)

// These tests turn "the docs drifted" into a build failure. They compare key
// sets only — tool names, flag names, config keys, command names, model
// aliases — never the prose describing them, so rewording a description is
// always free and adding or renaming a knob is never silent.

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	return root
}

func doc(t *testing.T, root, rel string) string {
	t.Helper()
	content, err := ReadDoc(root, rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return content
}

// reportDiff fails with the exact keys to add or remove and where.
func reportDiff(t *testing.T, what, docFile string, documented, actual map[string]bool) {
	t.Helper()
	missing, extra := Diff(actual, documented)
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("%s: undocumented in %s (add an entry for each): %s",
			what, docFile, strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		t.Errorf("%s: documented in %s but absent from the code (remove or rename): %s",
			what, docFile, strings.Join(extra, ", "))
	}
}

// firstCellTokens pulls the code-span token out of the first cell of each row.
func firstCellTokens(t *testing.T, rows [][]string) []string {
	t.Helper()
	var out []string
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		if tok := BacktickToken(row[0]); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// --- 1. Tools ---------------------------------------------------------------

// Tools registered outside a normal session build and therefore out of scope
// here: they never appear in the agent's advertised tool set the way the
// documented ones do.
//
//   - checkpoint: an internal overlay tool, visible only during the
//     pre-compaction turn (agent.SendPrepareCompact).
//   - submit_plan / request_review: registered and unregistered dynamically by
//     plan mode, documented in docs/tui.md's plan-mode section instead.
var knownUnregisterable = map[string]bool{
	"checkpoint":     true,
	"submit_plan":    true,
	"request_review": true,
}

func TestToolsDocumented(t *testing.T) {
	root := repoRoot(t)
	content := doc(t, root, "docs/tools.md")

	always, err := TableAfter(content, "Always registered:")
	if err != nil {
		t.Fatalf("docs/tools.md: %v", err)
	}
	conditional, err := TableAfter(content, "Conditionally registered:")
	if err != nil {
		t.Fatalf("docs/tools.md: %v", err)
	}
	documented := SetOf(append(firstCellTokens(t, always), firstCellTokens(t, conditional)...))

	reportDiff(t, "tools", "docs/tools.md", documented, actualToolNames(t))
}

// actualToolNames registers the same tools bootstrap.BuildSession does, with
// every conditional trigger switched on, and returns the registry's names. We
// re-do the registration here rather than calling BuildSession because that
// needs a provider and a real config; the registration calls are what matters.
func actualToolNames(t *testing.T) map[string]bool {
	t.Helper()
	workspace := t.TempDir()
	reg := core.NewRegistry()

	cfg := tool.ToolConfig{
		WorkspaceRoot: workspace,
		// Non-empty key registers web_search.
		BraveAPIKey: "test-key",
		BashJobs:    tool.NewBashJobs(context.Background(), nil, nil, nil),
	}
	if err := tool.RegisterBuiltins(reg, cfg); err != nil {
		t.Fatalf("register builtins: %v", err)
	}
	if err := tool.RegisterMemory(reg, tool.ToolConfig{
		WorkspaceRoot: workspace,
		MemoryStore:   memory.New(filepath.Join(workspace, "config"), workspace),
	}); err != nil {
		t.Fatalf("register memory: %v", err)
	}
	if err := reg.Register(tasks.NewTool(tasks.NewStore())); err != nil {
		t.Fatalf("register tasks: %v", err)
	}
	if err := reg.Register(verify.NewTool(workspace)); err != nil {
		t.Fatalf("register verify: %v", err)
	}
	if err := reg.Register(skill.NewTool([]skill.Skill{{Name: "example"}})); err != nil {
		t.Fatalf("register load_skill: %v", err)
	}
	if err := reg.Register(askuser.NewTool(askuser.NewBridge())); err != nil {
		t.Fatalf("register ask_user: %v", err)
	}
	if _, err := subagent.RegisterAll(reg, subagent.Config{
		AppCtx:        context.Background(),
		ParentTools:   reg,
		WorkspaceRoot: workspace,
	}); err != nil {
		t.Fatalf("register subagents: %v", err)
	}

	names := make(map[string]bool)
	for _, tl := range reg.All() {
		if !knownUnregisterable[tl.Name] {
			names[tl.Name] = true
		}
	}
	return names
}

// --- 2. CLI flags -----------------------------------------------------------

// cpuprofile is a development aid (pprof capture), not part of the user-facing
// surface docs/cli.md describes.
var undocumentedByDesign = map[string]bool{"cpuprofile": true}

func TestCLIFlagsDocumented(t *testing.T) {
	root := repoRoot(t)
	content := doc(t, root, "docs/cli.md")

	groups := []struct {
		what     string
		file     string
		receiver string
		anchor   string
	}{
		{"root flags", "cmd/moa/main.go", "flag", "## Flags"},
		{"moa update flags", "cmd/moa/cli_update.go", "fs", "## Update subcommand"},
		{"moa serve flags", "cmd/moa/cli_serve.go", "fs", "## Serve subcommand"},
	}

	for _, g := range groups {
		t.Run(g.what, func(t *testing.T) {
			names, err := GoFlagNames(filepath.Join(root, g.file), g.receiver)
			if err != nil {
				t.Fatalf("parse %s: %v", g.file, err)
			}
			actual := make(map[string]bool)
			for _, n := range names {
				if !undocumentedByDesign[n] {
					actual[n] = true
				}
			}

			rows, err := TablesInSection(content, g.anchor)
			if err != nil {
				t.Fatalf("docs/cli.md %q: %v", g.anchor, err)
			}
			documented := make(map[string]bool)
			for _, tok := range firstCellTokens(t, rows) {
				documented[strings.TrimLeft(tok, "-")] = true
			}

			reportDiff(t, g.what, "docs/cli.md "+g.anchor, documented, actual)
		})
	}
}

// --- 3. Config fields -------------------------------------------------------

func TestConfigFieldsDocumented(t *testing.T) {
	root := repoRoot(t)
	content := doc(t, root, "docs/configuration.md")

	rows, err := TablesInSection(content, "## Config fields")
	if err != nil {
		t.Fatalf("docs/configuration.md: %v", err)
	}
	documentedTop := make(map[string]bool)
	documentedPerms := make(map[string]bool)
	for _, key := range firstCellTokens(t, rows) {
		top, sub, nested := strings.Cut(key, ".")
		documentedTop[top] = true
		if nested && top == "permissions" {
			documentedPerms[sub] = true
		}
	}

	reportDiff(t, "config fields", "docs/configuration.md (## Config fields)",
		documentedTop, jsonTagSet(reflect.TypeOf(core.MoaConfig{})))
	reportDiff(t, "permissions.* fields", "docs/configuration.md (### Permissions)",
		documentedPerms, jsonTagSet(reflect.TypeOf(core.PermissionsConfig{})))
}

// jsonTagSet returns the wire names of a struct's fields: the documented key is
// the JSON key a user writes in config.json, not the Go field name.
func jsonTagSet(typ reflect.Type) map[string]bool {
	out := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
	}
	return out
}

// --- 4. Slash commands ------------------------------------------------------

// The palette (allCommands) and the dispatcher (handleCommand) are two
// different key sets and both are checked: the palette lists one canonical name
// per action, while the dispatcher is where aliases live (`case "exit",
// "quit":`). Comparing only the palette let a new, undocumented alias through —
// it appears in neither set the test looked at.
//
// A palette command needs a row of its own; an alias only needs to be mentioned
// somewhere in the table (docs/tui.md writes them in the description cell, as
// in "(alias `/back`)"), so the alias check scans every cell.
func TestSlashCommandsDocumented(t *testing.T) {
	root := repoRoot(t)
	content := doc(t, root, "docs/tui.md")

	names, err := GoStructSliceField(filepath.Join(root, "pkg/tui/cmdpalette.go"), "allCommands", "Name")
	if err != nil {
		t.Fatalf("parse pkg/tui/cmdpalette.go: %v", err)
	}
	palette := SetOf(names)

	dispatched, err := GoDispatchedCommandNames(
		filepath.Join(root, "pkg/tui/app_commands.go"), "handleCommand", "cmd")
	if err != nil {
		t.Fatalf("parse pkg/tui/app_commands.go: %v", err)
	}
	accepted := SetOf(dispatched)

	rows, err := TablesInSection(content, "## Slash commands")
	if err != nil {
		t.Fatalf("docs/tui.md: %v", err)
	}
	// documented: commands with their own row. mentioned: every /token in the
	// table, description cells included.
	documented := make(map[string]bool)
	mentioned := make(map[string]bool)
	for _, row := range rows {
		for i, cell := range row {
			for _, tok := range AllBacktickTokens(cell) {
				if !strings.HasPrefix(tok, "/") {
					continue
				}
				// Cells look like "/goal <objective> [flags]|stop|status".
				name, _, _ := strings.Cut(strings.TrimPrefix(tok, "/"), " ")
				mentioned[name] = true
				if i == 0 {
					documented[name] = true
				}
			}
		}
	}

	var missing, undocumentedAlias, unexpected []string
	for name := range palette {
		if !documented[name] {
			missing = append(missing, "/"+name)
		}
	}
	for name := range accepted {
		if !mentioned[name] {
			undocumentedAlias = append(undocumentedAlias, "/"+name)
		}
	}
	for name := range documented {
		if !accepted[name] {
			unexpected = append(unexpected, "/"+name)
		}
	}
	sort.Strings(missing)
	sort.Strings(undocumentedAlias)
	sort.Strings(unexpected)
	if len(missing) > 0 {
		t.Errorf("slash commands: in allCommands but without a row in docs/tui.md (## Slash commands): %s",
			strings.Join(missing, ", "))
	}
	if len(undocumentedAlias) > 0 {
		t.Errorf("slash commands: accepted by handleCommand but never mentioned in docs/tui.md (## Slash commands); give it a row, or mention it as an alias in the description of the command it aliases: %s",
			strings.Join(undocumentedAlias, ", "))
	}
	if len(unexpected) > 0 {
		t.Errorf("slash commands: documented in docs/tui.md but not accepted by handleCommand (remove or rename): %s",
			strings.Join(unexpected, ", "))
	}
}

// --- 5. Model aliases -------------------------------------------------------

func TestModelAliasesDocumented(t *testing.T) {
	root := repoRoot(t)
	content := doc(t, root, "docs/cli.md")

	actual, err := GoStringMap(filepath.Join(root, "pkg/core/models.go"), "modelAliases")
	if err != nil {
		t.Fatalf("parse pkg/core/models.go: %v", err)
	}

	rows, err := TablesInSection(content, "## Model aliases")
	if err != nil {
		t.Fatalf("docs/cli.md: %v", err)
	}
	documented := make(map[string]string, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		alias, target := BacktickToken(row[0]), BacktickToken(row[1])
		if alias != "" {
			documented[alias] = target
		}
	}

	// The target is compared too: an alias pointing at a different model than
	// documented is worse than an undocumented one — it is a false statement.
	var problems []string
	for alias, target := range actual {
		docTarget, ok := documented[alias]
		switch {
		case !ok:
			problems = append(problems, "missing: `"+alias+"` → `"+target+"`")
		case docTarget != target:
			problems = append(problems, "wrong target: `"+alias+"` resolves to `"+target+"`, documented as `"+docTarget+"`")
		}
	}
	for alias := range documented {
		if _, ok := actual[alias]; !ok {
			problems = append(problems, "stale: `"+alias+"` is documented but not in modelAliases")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Errorf("model aliases out of sync with docs/cli.md (## Model aliases):\n  %s",
			strings.Join(problems, "\n  "))
	}
}
