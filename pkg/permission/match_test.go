package permission

import (
	"testing"
)

func TestMatchPattern_ToolNameOnly(t *testing.T) {
	if !matchPattern("bash", "bash", "ls -la") {
		t.Error("bare tool name should match any args")
	}
	if !matchPattern("Bash", "bash", "ls -la") {
		t.Error("should be case-insensitive")
	}
	if matchPattern("write", "bash", "ls") {
		t.Error("different tool should not match")
	}
}

func TestMatchPattern_ExactCommand(t *testing.T) {
	if !matchPattern("Bash(npm install)", "bash", "npm install") {
		t.Error("exact command should match")
	}
	if matchPattern("Bash(npm install)", "bash", "npm install --save foo") {
		t.Error("exact pattern should not match longer command")
	}
}

func TestMatchPattern_ColonStar(t *testing.T) {
	// "npm:*" matches "npm", "npm install", "npm run build", etc.
	if !matchPattern("Bash(npm:*)", "bash", "npm install --save react") {
		t.Error("prefix:* should match command starting with prefix")
	}
	if !matchPattern("Bash(npm:*)", "bash", "npm") {
		t.Error("prefix:* should match exact prefix")
	}
	if matchPattern("Bash(npm:*)", "bash", "npx something") {
		t.Error("prefix:* should not match different command")
	}
	if !matchPattern("Bash(docker-compose exec:*)", "bash", "docker-compose exec web rails console") {
		t.Error("multi-word prefix should work")
	}
}

func TestMatchPattern_GlobStar(t *testing.T) {
	if !matchPattern("Write(*.go)", "write", "main.go") {
		t.Error("*.go should match main.go")
	}
	if matchPattern("Write(*.go)", "write", "main.py") {
		t.Error("*.go should not match main.py")
	}
}

func TestMatchPattern_PathGlob(t *testing.T) {
	// filepath.Match * doesn't cross path separators
	if !matchPattern("Edit(pkg/*)", "edit", "pkg/main.go") {
		t.Error("pkg/* should match pkg/main.go")
	}
	if matchPattern("Edit(pkg/*)", "edit", "pkg/sub/main.go") {
		t.Error("pkg/* should not match nested paths (use pkg/*/*)")
	}
}

func TestMatchPolicy_AllowList(t *testing.T) {
	patterns := []string{"Bash(git:*)", "Bash(npm:*)", "edit"}
	args := map[string]any{"command": "git status"}

	if !matchPolicy(patterns, "bash", args) {
		t.Error("should match git:* pattern")
	}
	if !matchPolicy(patterns, "edit", map[string]any{"path": "foo.go"}) {
		t.Error("bare edit should match")
	}
	if matchPolicy(patterns, "bash", map[string]any{"command": "rm -rf /"}) {
		t.Error("rm should not match any pattern")
	}
}

func TestMatchPolicy_EmptyList(t *testing.T) {
	if matchPolicy(nil, "bash", map[string]any{"command": "ls"}) {
		t.Error("nil patterns should never match")
	}
	if matchPolicy([]string{}, "bash", map[string]any{"command": "ls"}) {
		t.Error("empty patterns should never match")
	}
}

func TestGenerateAllowPattern_Bash(t *testing.T) {
	pat := GenerateAllowPattern("bash", map[string]any{"command": "npm install --save react"})
	if pat != "Bash(npm:*)" {
		t.Errorf("expected Bash(npm:*), got %s", pat)
	}
}

func TestGenerateAllowPattern_OtherTool(t *testing.T) {
	pat := GenerateAllowPattern("write", map[string]any{"path": "foo.go"})
	if pat != "write" {
		t.Errorf("expected write, got %s", pat)
	}
}
