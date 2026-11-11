package book

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
)

// The book has one writer and one door: the book tool, which enforces the size
// limits and the reserved files. A shell redirect into the directory skips both
// (the owner of one real project wrote its book through bash on every turn for
// weeks, past every warning), so the owner's own session refuses those calls
// before they run.
//
// This is a good-faith guard, not a sandbox. It reads the command the way a
// person would — following `cd`, `~`, $HOME and simple NAME=dir assignments —
// and cannot see through a script file, an unresolved variable or eval. Reading
// the book from a shell (grep, cat, sed -n, ls) is deliberately left alone.

// GuardMessage is what the model is told when a call is refused.
const GuardMessage = "The book is written only with the book tool (action write or append; read the file first with action read, and put detail in its own file rather than in PROJECT.md). Writing the book directory from a shell or with the file tools skips its size limits. Reading it with grep, cat or sed -n is fine."

// GuardExtension refuses, in an owner's session, the calls that would write the
// book directory without going through the book tool.
//
// cwd tells where the session's shell is when a command starts (its persisted
// directory, or the one the call names); nil means unknown.
func GuardExtension(dir string, cwd func(ctx context.Context, tool string, args map[string]any) string) extension.Extension {
	return guardExtension{dir: dir, cwd: cwd}
}

type guardExtension struct {
	dir string
	cwd func(ctx context.Context, tool string, args map[string]any) string
}

func (g guardExtension) Init(api extension.API) error {
	// The calls of one assistant turn are all checked before any of them runs,
	// so `cd BOOK` and the write after it arrive together while the shell is
	// still where it was. Every directory an earlier shell command here could
	// have left the shell in is therefore another place to start from. They are
	// forgotten as soon as the shell's own directory changes: that is the
	// signal that those commands have run and the real state is known again.
	var (
		mu           sync.Mutex
		lastProvided string
		ends         = map[string]bool{}
	)
	api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
		cwd := ""
		if g.cwd != nil {
			cwd = g.cwd(ctx, name, args)
		}
		if !strings.EqualFold(name, "bash") {
			if WritesBookFrom(g.dir, cwd, name, args) {
				return &core.ToolCallDecision{Block: true, Reason: GuardMessage}
			}
			return nil
		}
		explicit := false
		if c, _ := args["cwd"].(string); c != "" {
			explicit = true
		}
		mu.Lock()
		defer mu.Unlock()
		if cwd != lastProvided && !explicit {
			lastProvided = cwd
			ends = map[string]bool{}
		}
		starts := []string{cwd}
		if !explicit {
			for e := range ends {
				starts = append(starts, e)
			}
		}
		for _, start := range starts {
			hit, end := checkCall(g.dir, start, name, args)
			if hit {
				return &core.ToolCallDecision{Block: true, Reason: GuardMessage}
			}
			if !explicit && end != "" && end != cwd {
				ends[end] = true
			}
		}
		return nil
	})
	return nil
}

// WritesBook reports whether a tool call would write inside the book directory
// by a route other than the book tool.
func WritesBook(dir, tool string, args map[string]any) bool {
	return WritesBookFrom(dir, "", tool, args)
}

// WritesBookFrom is WritesBook for a shell that starts in cwd.
func WritesBookFrom(dir, cwd, tool string, args map[string]any) bool {
	hit, _ := checkCall(dir, cwd, tool, args)
	return hit
}

// checkCall is WritesBookFrom that also reports where a shell command would
// leave the shell ("" when unknown or not a shell command).
func checkCall(dir, cwd, tool string, args map[string]any) (hit bool, end string) {
	if dir == "" {
		return false, ""
	}
	g := newGuard(dir)
	if cwd != "" && filepath.IsAbs(cwd) {
		g.cwd = filepath.Clean(cwd)
	}
	switch strings.ToLower(tool) {
	case "bash":
		cmd, _ := args["command"].(string)
		hit = g.script(cmd, 0)
		return hit, g.cwd
	case "write", "edit", "multiedit":
		path, _ := args["path"].(string)
		return path != "" && g.inBook(g.resolve(path)), ""
	case "apply_patch":
		patch, _ := args["patch"].(string)
		for _, m := range patchFileRE.FindAllStringSubmatch(patch, -1) {
			if g.inBook(g.resolve(strings.TrimSpace(m[1]))) {
				return true, ""
			}
		}
	}
	return false, ""
}

var patchFileRE = regexp.MustCompile(`(?m)^\*\*\* (?:(?:Add|Update|Delete) File|Move to): (.+)$`)

type guard struct {
	dir  string // absolute, cleaned
	home string
	cwd  string // "" when unknown
	vars map[string]string
}

func newGuard(dir string) *guard {
	abs := dir
	if a, err := filepath.Abs(dir); err == nil {
		abs = a
	}
	home, _ := os.UserHomeDir()
	return &guard{dir: filepath.Clean(abs), home: home, vars: map[string]string{}}
}

func (g *guard) inBook(p string) bool {
	return p != "" && (p == g.dir || strings.HasPrefix(p, g.dir+"/"))
}

// resolve expands ~, $HOME and known variables and anchors a relative path at
// the tracked working directory. It returns "" when the path cannot be known.
func (g *guard) resolve(w string) string {
	w = g.expand(w)
	if w == "" {
		return ""
	}
	// A path that starts with an unresolved variable or substitution ($f from
	// `f=$(ls ...)`) is most often a name relative to the directory the command
	// already moved into; anywhere else it is unknowable.
	if w[0] == '$' || w[0] == '`' {
		if g.cwd != "" {
			return g.cwd + "/" + w
		}
		return ""
	}
	if !filepath.IsAbs(w) {
		if g.cwd == "" {
			return ""
		}
		w = g.cwd + "/" + w
	}
	return filepath.Clean(w)
}

var varRE = regexp.MustCompile(`\$\{(\w+)\}|\$(\w+)`)

func (g *guard) expand(w string) string {
	if w == "~" || strings.HasPrefix(w, "~/") {
		w = g.home + w[1:]
	}
	return varRE.ReplaceAllStringFunc(w, func(m string) string {
		name := strings.Trim(m, "${}")
		if name == "HOME" {
			return g.home
		}
		if v, ok := g.vars[name]; ok {
			return v
		}
		return m
	})
}

// mentionsBook is the cheap test for "this text is about the book": the
// directory in any of the spellings a person uses for it.
func (g *guard) mentionsBook(s string) bool {
	if strings.Contains(s, g.dir) {
		return true
	}
	if g.home != "" && strings.HasPrefix(g.dir, g.home+"/") {
		rel := g.dir[len(g.home):]
		return strings.Contains(s, "~"+rel) || strings.Contains(s, "$HOME"+rel) || strings.Contains(s, "${HOME}"+rel)
	}
	return false
}

// segment is one simple command: its words, the targets of its redirections
// and any heredoc bodies.
type segment struct {
	words   []string
	redirs  []string
	heredoc string
	depth   int      // nesting of ( ): a cd inside does not leave it
	subs    []string // bodies of $( ), run as scripts of their own
}

const maxGuardDepth = 3

func (g *guard) script(src string, depth int) bool {
	if depth > maxGuardDepth {
		return false
	}
	var saved []shellState
	level := 0
	for _, seg := range splitSegments(src) {
		for level < seg.depth {
			saved = append(saved, g.state())
			level++
		}
		for level > seg.depth && len(saved) > 0 {
			g.restore(saved[len(saved)-1])
			saved = saved[:len(saved)-1]
			level--
		}
		if g.segment(seg, depth) {
			return true
		}
	}
	return false
}

// shellState is what a subshell, $( ) or `bash -c` must not leak: the working
// directory and the variables.
type shellState struct {
	cwd  string
	vars map[string]string
}

func (g *guard) state() shellState {
	vars := make(map[string]string, len(g.vars))
	for k, v := range g.vars {
		vars[k] = v
	}
	return shellState{cwd: g.cwd, vars: vars}
}

func (g *guard) restore(s shellState) { g.cwd, g.vars = s.cwd, s.vars }

// nested runs a script of its own (a $( ) body, bash -c, a heredoc fed to a
// shell) without letting its cd or assignments out.
func (g *guard) nested(src string, depth int) bool {
	saved := g.state()
	defer g.restore(saved)
	return g.script(src, depth+1)
}

// shellKeywords introduce a command rather than being one.
var shellKeywords = map[string]bool{"then": true, "do": true, "else": true, "elif": true, "if": true, "while": true, "until": true, "!": true, "{": true, "}": true, "fi": true, "done": true}

var assignRE = regexp.MustCompile(`^([A-Za-z_]\w*)=(.*)$`)

func (g *guard) segment(seg segment, depth int) bool {
	for _, sub := range seg.subs {
		if g.nested(sub, depth) {
			return true
		}
	}
	for _, r := range seg.redirs {
		if g.inBook(g.resolve(r)) {
			return true
		}
	}
	words := seg.words
	// Leading NAME=value assignments: remember them, they are how a long path
	// gets spelled once.
	for len(words) > 0 {
		m := assignRE.FindStringSubmatch(words[0])
		if m == nil {
			break
		}
		if strings.Contains(m[2], "mktemp") {
			g.vars[m[1]] = "/\x00external"
		} else if p := g.resolve(m[2]); p != "" {
			g.vars[m[1]] = p
		}
		words = words[1:]
	}
	for len(words) > 0 && shellKeywords[words[0]] {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	cmd := filepath.Base(words[0])
	args := words[1:]
	switch cmd {
	case "export", "declare", "local", "readonly", "typeset":
		for _, a := range args {
			if m := assignRE.FindStringSubmatch(a); m != nil {
				if p := g.resolve(m[2]); p != "" {
					g.vars[m[1]] = p
				}
			}
		}
		return false
	case "cd", "pushd":
		if len(args) > 0 {
			g.cwd = g.resolve(args[len(args)-1])
		}
		return false
	case "sudo", "env", "nohup", "time", "command", "exec", "xargs", "nice":
		return g.segment(segment{words: skipOpts(args), heredoc: seg.heredoc}, depth)
	case "bash", "sh", "zsh", "dash":
		for i, a := range args {
			if (a == "-c" || (strings.HasPrefix(a, "-") && strings.HasSuffix(a, "c"))) && i+1 < len(args) {
				return g.nested(args[i+1], depth)
			}
		}
		// `bash <<'SH'`: the heredoc is the script.
		return seg.heredoc != "" && g.nested(seg.heredoc, depth)
	}

	ops := operands(args)
	switch cmd {
	case "tee", "rm", "touch", "truncate", "unlink", "shred", "mkdir", "rmdir", "ln", "install", "ed", "sponge":
		return g.anyInBook(ops)
	case "mv":
		// Swapping the whole directory (`mv -T new book`, how a snapshot is
		// restored) is not a content write; moving the book away never is.
		// Anything else into or out of the directory is.
		whole := false
		for _, a := range args {
			whole = whole || a == "-T" || a == "--no-target-directory"
		}
		for i, o := range ops {
			p := g.resolve(o)
			if p == g.dir && (i < len(ops)-1 || whole) {
				continue
			}
			if g.inBook(p) {
				return true
			}
		}
		return false
	case "cp", "rsync", "scp":
		if t := optionValue(args, "-t", "--target-directory"); t != "" {
			return g.inBook(g.resolve(t))
		}
		return len(ops) > 0 && g.inBook(g.resolve(ops[len(ops)-1]))
	case "dd":
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "of="); ok && g.inBook(g.resolve(v)) {
				return true
			}
		}
	case "sed", "perl", "awk", "gawk", "ruby":
		if hasInPlace(args) {
			if (cmd == "sed" || cmd == "perl") && !hasScriptOpt(args) && len(ops) > 0 {
				ops = ops[1:]
			}
			return g.anyInBook(ops)
		}
	case "find":
		for _, a := range args {
			if a == "-delete" {
				return g.anyInBook(ops)
			}
		}
		// find B -exec sed -i ... {} +: judge the command it runs, with {}
		// standing for what find was pointed at.
		var roots []string
		for _, o := range ops {
			if g.inBook(g.resolve(o)) {
				roots = append(roots, o)
			}
		}
		for i, a := range args {
			if a != "-exec" && a != "-execdir" && a != "-ok" || len(roots) == 0 {
				continue
			}
			var run []string
			for _, w := range args[i+1:] {
				if w == ";" || w == "+" {
					break
				}
				run = append(run, strings.ReplaceAll(w, "{}", roots[0]))
			}
			if g.segment(segment{words: run}, depth) {
				return true
			}
		}
	}
	if isInterpreter(cmd) {
		text := strings.Join(args, "\n") + "\n" + seg.heredoc
		if (g.inBook(g.cwd) || g.mentionsBook(text) || g.varsMentionBook(text)) && scriptWriteRE.MatchString(text) {
			return true
		}
	}
	return false
}

func (g *guard) varsMentionBook(text string) bool {
	for name, v := range g.vars {
		if g.inBook(filepath.Clean(v)) && (strings.Contains(text, "$"+name) || strings.Contains(text, "${"+name+"}")) {
			return true
		}
	}
	return false
}

func (g *guard) anyInBook(ops []string) bool {
	for _, o := range ops {
		if g.inBook(g.resolve(o)) {
			return true
		}
	}
	return false
}

func skipOpts(args []string) []string {
	for len(args) > 0 && (strings.HasPrefix(args[0], "-") || assignRE.MatchString(args[0])) {
		args = args[1:]
	}
	return args
}

// operands drops the options, which never name a path of their own.
func operands(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func optionValue(args []string, names ...string) string {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1]
			}
			if v, ok := strings.CutPrefix(a, n+"="); ok {
				return v
			}
		}
	}
	return ""
}

var inPlaceRE = regexp.MustCompile(`^-[A-Za-z]*i`)

func hasInPlace(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--in-place" || strings.HasPrefix(a, "--in-place="):
			return true
		case a == "inplace" && i > 0 && args[i-1] == "-i": // gawk -i inplace
			return true
		case !strings.HasPrefix(a, "--") && inPlaceRE.MatchString(a):
			return true
		}
	}
	return false
}

func hasScriptOpt(args []string) bool {
	for _, a := range args {
		if a == "-e" || a == "-f" || strings.HasPrefix(a, "--expression") || strings.HasPrefix(a, "--file") {
			return true
		}
	}
	return false
}

func isInterpreter(cmd string) bool {
	switch {
	case strings.HasPrefix(cmd, "python"), cmd == "node", cmd == "nodejs", cmd == "ruby", cmd == "perl", cmd == "php", cmd == "bun", cmd == "deno":
		return true
	}
	return false
}

// scriptWriteRE matches the calls a script uses to change a file. It is run
// only on scripts that are about the book.
var scriptWriteRE = regexp.MustCompile(`(?i)(\bopen\([^,()]*,\s*['"][rbt]*[wax+][^'"]*['"]|\.open\(\s*['"][rbt]*[wax+][bt+]*['"]|mode\s*=\s*['"][^'"]*[wax+]|write_text|write_bytes|writelines|os\.(rename|remove|unlink|replace|truncate)|shutil\.(copy|move|rmtree)|\.(unlink|rename|touch)\(|fs\.(write|append|rename|unlink|rm|copy)|writeFile|appendFile|File\.write|File\.open\([^)]*,\s*['"][^'"]*[wa+]|file_put_contents|\bsed -i)`)

// splitSegments tokenizes a shell script just enough for the guard: quotes,
// operators, redirections and heredocs. A quoted ">" is text, and the body of a
// heredoc is data (kept apart, because a script fed to an interpreter through
// one is still a script).
func splitSegments(src string) []segment {
	var (
		segs     []segment
		cur      segment
		word     strings.Builder
		inWord   bool
		pendRedi bool // the previous token was a bare redirection operator
		pending  []string
		nest     int
	)
	flushWord := func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		if pendRedi {
			cur.redirs = append(cur.redirs, w)
			pendRedi = false
			return
		}
		cur.words = append(cur.words, w)
	}
	endSeg := func() {
		flushWord()
		pendRedi = false
		if len(cur.words) > 0 || len(cur.redirs) > 0 {
			segs = append(segs, cur)
		}
		cur = segment{depth: nest}
	}
	rs := []rune(src)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < len(rs):
			if rs[i+1] == '\n' {
				i++
				continue
			}
			i++
			word.WriteRune(rs[i])
			inWord = true
		case c == '\'':
			inWord = true
			for i++; i < len(rs) && rs[i] != '\''; i++ {
				word.WriteRune(rs[i])
			}
		case c == '"':
			inWord = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) {
					i++
				} else if rs[i] == '$' && i+1 < len(rs) && rs[i+1] == '(' {
					start := i
					i = matchParen(rs, i+1)
					word.WriteString(string(rs[start : i+1]))
					if i > start+2 {
						cur.subs = append(cur.subs, string(rs[start+2:i]))
					}
					continue
				}
				word.WriteRune(rs[i])
			}
		case c == '#' && !inWord:
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			i--
		case c == '\n':
			endSeg()
			for _, delim := range pending {
				var body strings.Builder
				for i++; i < len(rs); {
					j := i
					for j < len(rs) && rs[j] != '\n' {
						j++
					}
					line := string(rs[i:j])
					i = j + 1
					if strings.TrimSpace(line) == delim {
						break
					}
					body.WriteString(line + "\n")
				}
				i--
				if n := len(segs); n > 0 {
					segs[n-1].heredoc += body.String()
				}
			}
			pending = nil
		case c == '$' && i+1 < len(rs) && rs[i+1] == '(':
			// Kept whole inside the word, so NAME=$(...) stays one assignment.
			start := i
			i = matchParen(rs, i+1)
			word.WriteString(string(rs[start : i+1]))
			inWord = true
			if i > start+2 {
				cur.subs = append(cur.subs, string(rs[start+2:i]))
			}
		case c == ';' || c == '|' || c == '&' || c == '(' || c == ')' || c == '`':
			if c == '&' && i+1 < len(rs) && rs[i+1] == '>' {
				// &> file
				flushWord()
				i++
				if i+1 < len(rs) && rs[i+1] == '>' {
					i++
				}
				pendRedi = true
				continue
			}
			endSeg()
			switch c {
			case '(':
				nest++
				cur.depth = nest
			case ')':
				if nest > 0 {
					nest--
				}
				cur.depth = nest
			}
		case c == '<':
			flushWord()
			if i+1 < len(rs) && rs[i+1] == '<' {
				i += 2
				if i < len(rs) && rs[i] == '<' { // here-string
					continue
				}
				if i < len(rs) && rs[i] == '-' {
					i++
				}
				for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
					i++
				}
				var d strings.Builder
				for ; i < len(rs) && !strings.ContainsRune(" \t\n;|&)", rs[i]); i++ {
					if rs[i] != '\'' && rs[i] != '"' && rs[i] != '\\' {
						d.WriteRune(rs[i])
					}
				}
				i--
				pending = append(pending, d.String())
			} else {
				// input redirection: its operand is read, so swallow it.
				for i+1 < len(rs) && rs[i+1] == ' ' {
					i++
				}
				for i+1 < len(rs) && !strings.ContainsRune(" \t\n;|&)", rs[i+1]) {
					i++
				}
			}
		case c == '>':
			// A digit-only word right before the operator is a file descriptor.
			if inWord && isDigits(word.String()) {
				word.Reset()
				inWord = false
			} else {
				flushWord()
			}
			if i+1 < len(rs) && rs[i+1] == '&' { // 2>&1
				i++
				for i+1 < len(rs) && (rs[i+1] >= '0' && rs[i+1] <= '9' || rs[i+1] == '-') {
					i++
				}
				continue
			}
			if i+1 < len(rs) && (rs[i+1] == '>' || rs[i+1] == '|') {
				i++
			}
			pendRedi = true
		case c == ' ' || c == '\t':
			flushWord()
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	endSeg()
	return segs
}

// matchParen returns the index of the ")" closing the "(" at open, or the last
// rune when it is never closed.
func matchParen(rs []rune, open int) int {
	level := 0
	for i := open; i < len(rs); i++ {
		switch rs[i] {
		case '(':
			level++
		case ')':
			if level--; level == 0 {
				return i
			}
		}
	}
	return len(rs) - 1
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
