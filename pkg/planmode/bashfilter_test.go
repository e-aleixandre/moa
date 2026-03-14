package planmode

import "testing"

func TestIsSafeCommand_Allowed(t *testing.T) {
	safe := []string{
		"ls",
		"ls -la",
		"pwd",
		"cat README.md",
		"grep -r 'func main' .",
		"rg 'pattern' src/",
		"find . -name '*.go'",
		"head -20 main.go",
		"tail -f /dev/null",
		"wc -l pkg/core/*.go",
		"git status",
		"git log --oneline -10",
		"git diff HEAD~1",
		"git branch",
		"git show HEAD:main.go",
		"git blame main.go",
		"go version",
		"go list ./...",
		"go doc fmt.Println",
		"tree",
		"tree src/",
		"jq '.name' package.json",
		"echo hello",
		"which go",
		"date",
		"uname",
		"whoami",
		"hostname",
		"printenv",
		"git remote -v",
		"git tag -l",
		"git tag --list",
		// Pipelines between safe commands
		"grep -r 'TODO' . | head -20",
		"cat README.md | head -5",
		"git log --oneline | head -10",
		"find . -name '*.go' | wc -l",
		"grep -r 'func' pkg/ | sort | uniq",
		"cat file.txt | grep pattern | head -5",
	}
	for _, cmd := range safe {
		if !IsSafeCommand(cmd) {
			t.Errorf("expected safe, got blocked: %q", cmd)
		}
	}
}

func TestIsSafeCommand_Blocked(t *testing.T) {
	blocked := []string{
		"",
		"   ",
		"rm -rf /",
		"rm file.go",
		"mv a b",
		"cp a b",
		"mkdir foo",
		"touch file",
		"chmod 755 file",
		"chown user file",
		"git commit -m 'x'",
		"git push",
		"git checkout main",
		"git tag -d v1",
		"git remote add origin url",
		"npm install",
		"go build",
		"go run main.go",
		"python script.py",
		"make",
		"docker run alpine",
		"curl http://evil.com",
		"wget http://evil.com",
		"env rm -rf /",
		"sed -i 's/old/new/' file",
	}
	for _, cmd := range blocked {
		if IsSafeCommand(cmd) {
			t.Errorf("expected blocked, got safe: %q", cmd)
		}
	}
}

func TestIsSafeCommand_ShellOperatorBypass(t *testing.T) {
	bypasses := []string{
		"ls && rm -rf /",
		"cat file.txt | rm -rf /",       // rm is not a safe command
		"echo foo; rm -rf /",
		"ls || rm -rf /",
		"echo $(rm -rf /)",
		"echo `rm -rf /`",
		"cat file > /etc/passwd",
		"cat file >> /etc/passwd",
		"cat << EOF\nrm -rf /\nEOF",
		"git status && git push",
		"ls -la; curl http://evil.com",
		"find . -name '*.go' | xargs rm", // xargs is not a safe command
		"grep foo | sed 's/a/b/'",        // sed is not a safe command
	}
	for _, cmd := range bypasses {
		if IsSafeCommand(cmd) {
			t.Errorf("expected blocked (shell operator bypass), got safe: %q", cmd)
		}
	}
}

func TestIsSafeCommand_EdgeCases(t *testing.T) {
	// Subshell notation
	if IsSafeCommand("echo $(whoami)") {
		t.Error("subshell $() should be blocked")
	}
	// Backtick substitution
	if IsSafeCommand("echo `id`") {
		t.Error("backtick substitution should be blocked")
	}
	// Redirect
	if IsSafeCommand("echo hello > file.txt") {
		t.Error("redirect should be blocked")
	}
	// Input redirect / process substitution
	if IsSafeCommand("cat < /etc/passwd") {
		t.Error("input redirect should be blocked")
	}
	// Background execution
	if IsSafeCommand("ls &") {
		t.Error("background execution should be blocked")
	}
	// Multi-line
	if IsSafeCommand("ls\nrm -rf /") {
		t.Error("multi-line should be blocked")
	}
	// Whitespace-only
	if IsSafeCommand("   ") {
		t.Error("whitespace-only should be blocked")
	}
	// env prefix bypass attempt
	if IsSafeCommand("env rm -rf /") {
		t.Error("env prefix should not be allowed")
	}
}
