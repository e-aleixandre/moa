package attention

import (
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/bus"
)

// spoken.go — deterministic "written for the ear" templates.
//
// Phase 1A redaction is 100% deterministic (no model). Language follows the
// server's configured STT language (en/es; "auto" -> en), so briefings match
// the user's spoken language (design §7). Every template prefixes the session
// alias so audio-only users always know which conversation an item is about.
//
// Fidelity rule (design §3.3): questions are relayed as-is; permission text
// always states the risk in strong words and never softens it.

// lang is the resolved briefing language. Only en/es in Phase 1A.
type lang string

const (
	langEN lang = "en"
	langES lang = "es"
)

// resolveLang maps an ISO-639-1 hint (from core.GetSTTLanguage) to a supported
// briefing language. Unknown or "auto" falls back to English (we don't invent
// detection in v1).
func resolveLang(code string) lang {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "es":
		return langES
	default:
		return langEN
	}
}

// spokenAsk writes the briefing for a pending question, including the question
// text joined when multiple questions are pending.
func (l lang) spokenAsk(alias string, questions []bus.AskQuestion) string {
	var qs []string
	for _, q := range questions {
		if q.Text != "" {
			qs = append(qs, q.Text)
		}
	}
	joined := strings.Join(qs, " — ")
	switch l {
	case langES:
		return fmt.Sprintf("En %s, el agente te pregunta: %s", alias, joined)
	default:
		return fmt.Sprintf("In %s, the agent is asking: %s", alias, joined)
	}
}

// spokenPermission writes the briefing for a permission request. It always
// states the tool and, when risk flags are present, names the danger in strong
// words. It does NOT read the full command by default (the client can offer
// that via Verbatim).
func (l lang) spokenPermission(alias, toolName string, level RiskLevel, flags []string) string {
	danger := dangerPhrase(l, flags)
	switch l {
	case langES:
		base := fmt.Sprintf("En %s, el agente pide permiso para usar %s", alias, toolName)
		if danger != "" {
			base += " — " + danger
		}
		return base + ". ¿Lo autorizas?"
	default:
		base := fmt.Sprintf("In %s, the agent wants permission to use %s", alias, toolName)
		if danger != "" {
			base += " — " + danger
		}
		return base + ". Approve?"
	}
}

// spokenError writes the briefing for a session error.
func (l lang) spokenError(alias, msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 160 {
		msg = msg[:160] + "…"
	}
	switch l {
	case langES:
		if msg == "" {
			return fmt.Sprintf("En %s, el agente ha dado un error.", alias)
		}
		return fmt.Sprintf("En %s, el agente ha dado un error: %s", alias, msg)
	default:
		if msg == "" {
			return fmt.Sprintf("In %s, the agent hit an error.", alias)
		}
		return fmt.Sprintf("In %s, the agent hit an error: %s", alias, msg)
	}
}

// dangerPhrase renders the risk flags as a short, strong spoken clause. Order is
// fixed so output is deterministic and testable. Empty when no flags.
func dangerPhrase(l lang, flags []string) string {
	if len(flags) == 0 {
		return ""
	}
	// Fixed severity order.
	order := []string{
		flagDestructive, flagGitForce, flagNetwork, flagSudo,
		flagRemote, flagProd, flagInstall, flagSecrets,
	}
	labels := dangerLabels(l)
	var parts []string
	for _, f := range order {
		for _, have := range flags {
			if have == f {
				parts = append(parts, labels[f])
				break
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	switch l {
	case langES:
		return "cuidado: " + strings.Join(parts, ", ")
	default:
		return "warning: " + strings.Join(parts, ", ")
	}
}

func dangerLabels(l lang) map[string]string {
	if l == langES {
		return map[string]string{
			flagDestructive: "borra o sobrescribe datos",
			flagGitForce:    "reescribe el historial de git",
			flagNetwork:     "ejecuta contenido descargado de internet",
			flagSudo:        "usa permisos de superusuario",
			flagRemote:      "actúa en un servidor remoto",
			flagProd:        "afecta a producción",
			flagInstall:     "instala paquetes",
			flagSecrets:     "toca credenciales o secretos",
		}
	}
	return map[string]string{
		flagDestructive: "deletes or overwrites data",
		flagGitForce:    "rewrites git history",
		flagNetwork:     "runs content downloaded from the internet",
		flagSudo:        "uses superuser privileges",
		flagRemote:      "acts on a remote server",
		flagProd:        "affects production",
		flagInstall:     "installs packages",
		flagSecrets:     "touches credentials or secrets",
	}
}

// -- Phase 2 progress/terminal templates ------------------------------------
//
// These are EPHEMERAL briefings (progress narration), not P0 items. The Spoken
// text for a run's result uses a deterministic fallback: the first sentence of
// the final text with markdown stripped, truncated (design §3.4). No LLM in
// Phase 2 — the model summary is Phase 4.

// spokenRunOK narrates a successfully finished run. hadEdits colors the wording
// ("made changes" vs "finished"). finalText is summarized deterministically.
func (l lang) spokenRunOK(alias, finalText string, hadEdits bool) string {
	summary := firstSentence(finalText, 140)
	switch l {
	case langES:
		verb := "ha terminado"
		if hadEdits {
			verb = "ha terminado y ha hecho cambios"
		}
		if summary == "" {
			return fmt.Sprintf("En %s, el agente %s.", alias, verb)
		}
		return fmt.Sprintf("En %s, el agente %s: %s", alias, verb, summary)
	default:
		verb := "finished"
		if hadEdits {
			verb = "finished and made changes"
		}
		if summary == "" {
			return fmt.Sprintf("In %s, the agent %s.", alias, verb)
		}
		return fmt.Sprintf("In %s, the agent %s: %s", alias, verb, summary)
	}
}

// terminationSummary retains a compact text preview for a durable completion
// notice. It strips fenced/code-diff sections before applying a byte (not rune)
// bound so the wire contract remains small even for very long agent answers.
func terminationSummary(s string) string {
	var kept []string
	inFence := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if strings.HasPrefix(trimmed, "diff --git") {
			break
		}
		if inFence || strings.HasPrefix(trimmed, "@@") ||
			strings.HasPrefix(trimmed, "+++") || strings.HasPrefix(trimmed, "---") ||
			(strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+ ")) ||
			(strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "- ")) {
			continue
		}
		kept = append(kept, line)
	}
	s = strings.Join(kept, "\n")
	return truncateUTF8Bytes(strings.TrimSpace(stripMarkdown(s)), 512)
}

func truncateUTF8Bytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && (s[end]&0xc0) == 0x80 {
		end--
	}
	return strings.TrimSpace(s[:end]) + "…"
}

// spokenGoalEnded narrates a goal loop stopping.
func (l lang) spokenGoalEnded(alias, reason string) string {
	reason = firstSentence(reason, 140)
	switch l {
	case langES:
		if reason == "" {
			return fmt.Sprintf("En %s, el objetivo ha terminado.", alias)
		}
		return fmt.Sprintf("En %s, el objetivo ha terminado: %s", alias, reason)
	default:
		if reason == "" {
			return fmt.Sprintf("In %s, the goal loop has ended.", alias)
		}
		return fmt.Sprintf("In %s, the goal loop has ended: %s", alias, reason)
	}
}

// spokenGoalStalled narrates a goal making no progress across iterations.
func (l lang) spokenGoalStalled(alias string, stalled int) string {
	switch l {
	case langES:
		return fmt.Sprintf("En %s, el objetivo lleva %d iteraciones sin avanzar. Quizá necesite tu ayuda.", alias, stalled)
	default:
		return fmt.Sprintf("In %s, the goal has made no progress for %d iterations. It may need your help.", alias, stalled)
	}
}

// spokenVerifyFail narrates auto-verify reporting failures. The summary is the
// formatted failure text; we deterministically shorten it.
func (l lang) spokenVerifyFail(alias, summary string) string {
	summary = firstSentence(summary, 140)
	switch l {
	case langES:
		if summary == "" {
			return fmt.Sprintf("En %s, la verificación automática ha fallado.", alias)
		}
		return fmt.Sprintf("En %s, la verificación automática ha fallado: %s", alias, summary)
	default:
		if summary == "" {
			return fmt.Sprintf("In %s, auto-verify reported failures.", alias)
		}
		return fmt.Sprintf("In %s, auto-verify reported failures: %s", alias, summary)
	}
}

// firstSentence strips markdown noise and returns the first sentence of s,
// truncated to max runes. Deterministic (no model). Used as the Phase 2/3
// fallback wording for run/goal results (design §3.4).
func firstSentence(s string, max int) string {
	s = stripMarkdown(s)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Cut at the first sentence terminator followed by space/end.
	for i, r := range s {
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			cut := strings.TrimSpace(s[:i])
			if len([]rune(cut)) >= 12 { // avoid cutting on "e.g." etc. too early
				s = cut
				break
			}
		}
	}
	runes := []rune(s)
	if len(runes) > max {
		return strings.TrimSpace(string(runes[:max])) + "…"
	}
	return s
}

// stripMarkdown removes code fences, inline code, and common markdown markers
// so a spoken summary doesn't read symbols aloud. Deterministic.
func stripMarkdown(s string) string {
	// Drop fenced code blocks entirely.
	for {
		start := strings.Index(s, "```")
		if start < 0 {
			break
		}
		end := strings.Index(s[start+3:], "```")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + " " + s[start+3+end+3:]
	}
	repl := strings.NewReplacer(
		"`", "", "#", "", "*", "", "_", "", ">", "", "|", " ",
		"[", "", "]", "", "\t", " ",
	)
	s = repl.Replace(s)
	// Collapse whitespace.
	return strings.Join(strings.Fields(s), " ")
}
