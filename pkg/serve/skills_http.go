package serve

import (
	"net/http"
	"strings"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"

	"github.com/e-aleixandre/moa/pkg/skill"
)

// SkillCommand is one user-invocable skill as the slash menu needs it.
type SkillCommand struct {
	// Name is what the user types after "/". It carries the "skill:" prefix
	// when the bare name would collide with a built-in command.
	Name string `json:"name"`
	// Skill is the skill's own name, without any prefix.
	Skill       string `json:"skill"`
	Description string `json:"description"`
}

// reservedCommandNames are names a skill may not take over: the server's
// command registry plus the commands a frontend implements on its own (/secret
// never reaches the server — the composer turns it into a stashed value).
var reservedCommandNames = func() map[string]bool {
	names := map[string]bool{"secret": true}
	for name := range commandRegistry {
		names[name] = true
	}
	return names
}()

// skillCommands turns discovered skills into slash-menu entries.
//
// A skill named like a built-in keeps its own entry, but under "skill:<name>":
// the built-in wins the bare name. Shadowing /compact or /undo with a file
// dropped in a skills directory would take away a command the user relies on,
// and the failure would be silent — the menu would look right.
func skillCommands(skills []skill.Skill) []SkillCommand {
	out := make([]SkillCommand, 0, len(skills))
	for _, s := range skills {
		if !s.UserInvocable {
			continue
		}
		name := s.Name
		if reservedCommandNames[name] {
			name = "skill:" + s.Name
		}
		desc := s.Description
		if desc == "" {
			desc = s.DisplayName
		}
		out = append(out, SkillCommand{Name: name, Skill: s.Name, Description: desc})
	}
	return out
}

// findInvocableSkill resolves a slash name to a user-invocable skill. It accepts
// both the bare name and the "skill:" form, which is how a skill whose name
// collides with a built-in is reached.
//
// Discovery reads the directory on every call rather than a set captured when
// the session was built, so a skill created mid-session is invocable right away.
func findInvocableSkill(cwd, name string) (skill.Skill, bool) {
	bare := strings.TrimPrefix(name, "skill:")
	for _, s := range skill.Discover(cwd) {
		if s.Name == bare && s.UserInvocable {
			return s, true
		}
	}
	return skill.Skill{}, false
}

// runSkillCommand loads a skill into the conversation as a user message.
//
// The content enters the transcript rather than the system prompt: it is what
// the user asked for now, it stays visible in the conversation, and it leaves
// the cached prefix untouched.
func runSkillCommand(sess *ManagedSession, s skill.Skill, args []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	body, err := skill.Load(s)
	if err != nil {
		return &CommandResult{OK: false, Message: "could not read skill " + s.Name + ": " + err.Error()}, nil
	}

	msg := core.AgentMessage{
		Message: core.Message{
			Role:    "user",
			Content: []core.Content{core.TextContent(renderSkillBody(body, args))},
		},
		Custom: map[string]any{"skill": s.Name},
	}
	if err := sess.runtime.Bus.Execute(bus.AppendToConversation{SessionID: sess.ID, Message: msg}); err != nil {
		return nil, err
	}
	return &CommandResult{OK: true, Message: "loaded skill: " + s.Name}, nil
}

// renderSkillBody substitutes the invocation's arguments into the skill.
//
// A skill that declares no placeholder still receives what the user typed,
// appended as a trailing line: dropping it silently would lose the only part of
// the invocation the user wrote by hand.
func renderSkillBody(body string, args []string) string {
	joined := strings.Join(args, " ")
	if strings.Contains(body, "$ARGUMENTS") {
		return strings.ReplaceAll(body, "$ARGUMENTS", joined)
	}
	if joined == "" {
		return body
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + "\nARGUMENTS: " + joined + "\n"
}

// handleSessionSkills lists the skills the user may invoke in a session.
//
// Skills are read from disk per request rather than from a set captured when
// the session was built, so a skill created mid-session shows up without a
// reload — discovery is a directory scan, and the menu is opened by hand.
func handleSessionSkills(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"skills": skillCommands(skill.Discover(sess.CWD)),
		})
	}
}
