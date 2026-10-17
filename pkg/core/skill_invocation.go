package core

import "strings"

// IsReservedSlashName reports whether a bare /name belongs to a built-in
// command, which wins over a skill of the same name. The command registry lives
// in the serve layer, which sets this once at start-up; until then nothing is
// reserved.
var IsReservedSlashName = func(string) bool { return false }

// SkillInvocation reports whether a message's Custom marks it as the user
// invoking an inline skill with /<name>, and what the user typed to do it.
//
// Messages written before skills started a run carry only custom.skill, with
// no source; they are the same event. A forked skill's anchor has its own
// source and is not an invocation of this kind.
func SkillInvocation(custom map[string]any) (command string, ok bool) {
	name, _ := custom["skill"].(string)
	if name == "" {
		return "", false
	}
	source, _ := custom["source"].(string)
	if source != "" && source != "skill" {
		return "", false
	}
	if cmd, _ := custom["skill_command"].(string); cmd != "" {
		return cmd, true
	}
	// Records written before skill_command existed: a skill that shares a
	// built-in's name was invoked as /skill:<name>, and resending the bare name
	// would run the built-in instead.
	if IsReservedSlashName(strings.ToLower(name)) {
		name = "skill:" + name
	}
	command = "/" + name
	if args, _ := custom["skill_args"].(string); strings.TrimSpace(args) != "" {
		command += " " + args
	}
	return command, true
}
