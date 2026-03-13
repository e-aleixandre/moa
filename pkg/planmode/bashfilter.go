package planmode

import "strings"

// IsSafeCommand checks whether a bash command is safe for planning mode.
// Two-layer defense:
//  1. Reject any shell control operators (pipes, chains, subshells, redirects).
//  2. Match against a known-safe command prefix allowlist.
func IsSafeCommand(command string) bool {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}
	if hasShellOperators(cmd) {
		return false
	}
	return matchesSafePrefix(cmd)
}

// shellOperators are tokens that enable command chaining, piping, output
// redirection, or subshell execution. Presence means we can't reason about
// what the command does, so we reject outright.
var shellOperators = []string{
	"&&", "||", "|", ";",
	"$(", "`",
	">>", ">",
	"<<",
	"&",  // background execution
	"<",  // input redirection / process substitution <(...)
	"\n", // multi-line
}

func hasShellOperators(cmd string) bool {
	for _, op := range shellOperators {
		if strings.Contains(cmd, op) {
			return true
		}
	}
	return false
}

// safePrefixes are commands (or command prefixes) known to be read-only.
// Sorted roughly by expected frequency.
var safePrefixes = []string{
	"cat ",
	"grep ", "grep\t",
	"rg ", "rg\t",
	"ls ", "ls\t", "ls\n",
	"find ", "find\t",
	"head ", "head\t",
	"tail ", "tail\t",
	"wc ", "wc\t",
	"file ", "file\t",
	"stat ", "stat\t",
	"du ", "du\t",
	"df ", "df\t",
	"pwd",
	"echo ",
	"which ", "which\t",
	"type ", "type\t",
	"printenv",
	"printenv ",
	"uname",
	"whoami",
	"hostname",
	"date",
	"go version", "go env", "go list ", "go doc ",
	"node --version", "node -v", "npm --version", "npm ls", "npm list",
	"python --version", "python3 --version",
	"git status", "git log ", "git log\n",
	"git diff ", "git diff\n",
	"git show ", "git show\n",
	"git branch", // git branch alone = list branches (safe)
	"git branch ", // git branch -a, git branch -r (list variants)
	"git rev-parse ", "git ls-files",
	"git describe", "git blame ",
	"git remote -v", "git remote show ",
	"git tag -l", "git tag --list",
	"tree ", "tree\t",
	"jq ", "jq\t",
	"awk ", "awk\t",
	"sort ", "sort\t",
	"uniq ", "uniq\t",
	"cut ", "cut\t",
	"diff ", "diff\t",
	"comm ", "comm\t",
	"test ", "[ ",
	"dirname ", "basename ",
	"realpath ", "readlink ",
	"sha256sum ", "md5sum ",
	"xxd ", "hexdump ",
}

// exactSafe are commands that are safe with no arguments.
var exactSafe = []string{
	"ls", "pwd", "printenv", "uname", "whoami", "hostname", "date",
	"git status", "git branch", "git log", "git diff",
	"git ls-files", "git describe",
	"tree",
}

func matchesSafePrefix(cmd string) bool {
	for _, exact := range exactSafe {
		if cmd == exact {
			return true
		}
	}
	for _, prefix := range safePrefixes {
		if strings.HasPrefix(cmd, prefix) {
			return true
		}
	}
	return false
}
