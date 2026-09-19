package skill

import (
	"embed"
	"io/fs"
	"path"
	"sync"
)

// builtinFS holds the skills that ship inside the binary.
//
// A skill kept only in ~/.config/moa/skills is a skill that exists on one
// machine. The owner's role prompt tells it to load book-init when its book is
// still the template, and a promise that depends on someone having copied a
// file by hand is a broken promise: what moa asks for, moa carries.
//
// These are deliberately few. A built-in is right when moa itself relies on the
// skill existing; anything else belongs to the user's own directory, where they
// can edit it without waiting for a release.
//
//go:embed builtin
var builtinFS embed.FS

const builtinRoot = "builtin"

// builtinPath is where a built-in skill's SKILL.md lives inside builtinFS.
func builtinPath(name string) string {
	return path.Join(builtinRoot, name, skillFile)
}

var (
	builtinOnce   sync.Once
	builtinParsed map[string]Skill
)

// builtinSkills parses the embedded skills once. The content cannot change
// while the process runs, so unlike the disk scan there is nothing to re-read.
func builtinSkills() map[string]Skill {
	builtinOnce.Do(func() {
		builtinParsed = map[string]Skill{}
		entries, err := fs.ReadDir(builtinFS, builtinRoot)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			data, err := builtinFS.ReadFile(builtinPath(e.Name()))
			if err != nil {
				continue
			}
			s := newSkill(e.Name(), string(data))
			s.Builtin = true
			builtinParsed[e.Name()] = s
		}
	})
	return builtinParsed
}
