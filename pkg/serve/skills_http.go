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

// skillCommandPrefix marks a skill whose own name is taken by a built-in.
const skillCommandPrefix = "skill:"

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

// invocableAs returns the slash name for a skill, and whether it can be
// invoked at all.
//
// A name is unusable when it cannot survive the round trip through the command
// line: whitespace would be split by the parser, and a name already carrying
// the disambiguation prefix could not be told apart from a prefixed collision.
// Advertising such a skill would show an entry that does nothing when clicked.
func invocableAs(s skill.Skill) (string, bool) {
	if s.Name == "" || strings.ContainsAny(s.Name, " \t\n") {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(s.Name), skillCommandPrefix) {
		return "", false
	}
	// Command lookup is case-insensitive, so a skill named "Compact" collides
	// with /compact just as "compact" does.
	if reservedCommandNames[strings.ToLower(s.Name)] {
		return skillCommandPrefix + s.Name, true
	}
	return s.Name, true
}

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
		name, ok := invocableAs(s)
		if !ok {
			continue
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
func findInvocableSkill(cwd, typed string) (skill.Skill, bool) {
	want := strings.ToLower(typed)
	for _, s := range skill.Discover(cwd) {
		if !s.UserInvocable {
			continue
		}
		// Match against the name the skill is actually offered under, so the
		// menu and the parser can never disagree.
		if name, ok := invocableAs(s); ok && strings.ToLower(name) == want {
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
	// Appending mutates the agent's state in memory; persistence and the web's
	// re-render both hang off the re-sync that CommandExecuted triggers. Without
	// this the skill is loaded for the live run but lost on restart.
	sess.runtime.Bus.Publish(bus.CommandExecuted{
		SessionID: sess.ID,
		Command:   "skill",
		Messages:  sess.runtime.Context().Agent.Messages(),
	})
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
