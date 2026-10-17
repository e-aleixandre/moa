package core

import "strings"

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
	command = "/" + name
	if args, _ := custom["skill_args"].(string); strings.TrimSpace(args) != "" {
		command += " " + args
	}
	return command, true
}
