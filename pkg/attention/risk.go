package attention

import (
	"regexp"
	"strings"
)

// risk.go — deterministic risk assessment for permission commands.
//
// This is a SECURITY-CRITICAL, LLM-FREE classifier. Its job is to detect danger
// signals in a shell command and surface them as flags that no downstream
// summarizer may ever remove (design §3.3). It deliberately OVER-classifies:
// when in doubt, escalate. A false "high" is annoying; a false "low" on an
// `rm -rf` is dangerous.
//
// Phase 1A uses this for the spoken text and the risk fields on AttentionItem.
// The future natural-language command summary (Phase 5) may only improve wording
// AROUND these flags, never contradict them.

// Risk flag identifiers. Stable strings — clients and tests depend on them.
const (
	flagDestructive = "destructive" // deletes/overwrites/truncates data
	flagRemote      = "remote"      // acts on another host (ssh/scp/rsync remote)
	flagSudo        = "sudo"        // elevates privileges
	flagProd        = "prod"        // mentions a production-looking target
	flagGitForce    = "git-force"   // force push / history rewrite
	flagInstall     = "install"     // installs packages / changes system state
	flagNetwork     = "network"     // pipes network content into a shell, etc.
	flagSecrets     = "secrets"     // touches credential-looking paths/vars
)

var (
	// Destructive: rm, rmdir, deletion flags, truncation/overwrite redirects,
	// destructive git/docker/db verbs, mkfs, dd.
	reDestructive = regexp.MustCompile(`(?i)\b(rm|rmdir|unlink|shred|mkfs\S*|dd)\b|` +
		`--delete\b|--force\b|\bfind\b[^|;]*-delete\b|\btruncate\b|` +
		`\b(drop|truncate)\s+(table|database)\b|` +
		`\bgit\s+(reset\s+--hard|clean\s+-[a-z]*f[a-z]*)\b|` +
		`\bdocker\s+(rm|rmi|system\s+prune)\b|` +
		`>\s*/|:\s*>\s*\S`)

	// Remote execution / transfer to another host.
	reRemote = regexp.MustCompile(`(?i)\b(ssh|scp|sftp|rsync)\b|\bdocker\s+-H\b|\bkubectl\b`)

	reSudo = regexp.MustCompile(`(?i)\b(sudo|doas|su)\b`)

	// Production-looking targets. Conservative: matches common prod tokens.
	reProd = regexp.MustCompile(`(?i)\b(prod|production|prd|live)\b`)

	// Force push / history rewrite.
	reGitForce = regexp.MustCompile(`(?i)\bgit\s+push\b[^|;&]*(--force\b|-f\b|--force-with-lease\b)|` +
		`\bgit\s+push\s+--mirror\b`)

	// Package / system-state installers.
	reInstall = regexp.MustCompile(`(?i)\b(apt|apt-get|yum|dnf|pacman|brew|pip|pip3|npm|pnpm|yarn|go)\s+(install|add|get|i)\b|` +
		`\bnpm\s+i\b|\bcurl\b[^|]*\|\s*(sudo\s+)?(bash|sh)\b`)

	// Network content piped into a shell/interpreter, or fetch-and-run.
	reNetwork = regexp.MustCompile(`(?i)\b(curl|wget|fetch)\b[^|;]*\|\s*(sudo\s+)?(bash|sh|python|perl|node)\b`)

	// Credential-looking paths / env.
	reSecrets = regexp.MustCompile(`(?i)(\.env\b|/\.ssh/|id_rsa\b|\.pem\b|credentials\b|secrets?\b|\.aws/|\.netrc\b|token\b|password\b)`)
)

// assessRisk inspects a tool name and its args and returns a conservative risk
// level plus the set of danger flags found. It is pure and deterministic.
//
// The command string is reconstructed from common arg shapes: a "command"
// string (bash), a "cmd"/"script" field, or a join of stringy values. This is
// heuristic by nature — hence the over-classification bias.
func assessRisk(toolName string, args map[string]any) (RiskLevel, []string) {
	cmd := commandString(toolName, args)
	if cmd == "" {
		// Nothing to inspect. Non-command tools (read/edit/write) are handled by
		// the caller; if we truly can't tell, be cautious but not alarmist.
		return RiskLow, nil
	}

	var flags []string
	add := func(f string) { flags = append(flags, f) }

	if reDestructive.MatchString(cmd) {
		add(flagDestructive)
	}
	if reRemote.MatchString(cmd) {
		add(flagRemote)
	}
	if reSudo.MatchString(cmd) {
		add(flagSudo)
	}
	if reProd.MatchString(cmd) {
		add(flagProd)
	}
	if reGitForce.MatchString(cmd) {
		add(flagGitForce)
	}
	if reInstall.MatchString(cmd) {
		add(flagInstall)
	}
	if reNetwork.MatchString(cmd) {
		add(flagNetwork)
	}
	if reSecrets.MatchString(cmd) {
		add(flagSecrets)
	}

	return levelFor(flags), flags
}

// levelFor maps the set of flags to a conservative overall level.
//   - high:   destructive, git-force, network-piped-to-shell, or (remote AND prod)
//   - medium: any single elevated/remote/prod/install/secrets signal
//   - low:    no flags
func levelFor(flags []string) RiskLevel {
	if len(flags) == 0 {
		return RiskLow
	}
	has := func(f string) bool {
		for _, x := range flags {
			if x == f {
				return true
			}
		}
		return false
	}
	if has(flagDestructive) || has(flagGitForce) || has(flagNetwork) {
		return RiskHigh
	}
	if has(flagRemote) && has(flagProd) {
		return RiskHigh
	}
	return RiskMedium
}

// commandString reconstructs an inspectable command string from tool args.
// Recognizes the common shapes moa tools use; falls back to joining string
// values so an unusual tool still gets scanned rather than silently passing.
func commandString(toolName string, args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, key := range []string{"command", "cmd", "script", "code"} {
		if v, ok := args[key].(string); ok && v != "" {
			return v
		}
	}
	// Fallback: join stringy values so nothing escapes the scan unseen.
	var parts []string
	for _, v := range args {
		if s, ok := v.(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}
