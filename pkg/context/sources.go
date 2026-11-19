package agentcontext

import (
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/git"
	"github.com/e-aleixandre/moa/pkg/memory"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/skill"
)

// Sources holds the prompt inputs that live on disk, so they can be re-read
// while a session is open.
//
// These were captured by value when a session was built, which made a session
// the only place they existed: editing AGENTS.md or adding a memory had no
// effect until a restart. Keeping them behind one guarded holder lets a reload
// update every consumer — the base prompt builder, new subagents — from a
// single place, instead of each one owning a copy that drifts.
type Sources struct {
	mu          sync.RWMutex
	cwd         string
	memStore    *memory.Store
	agentsMD    string
	skillsIndex string
	memoryIndex string
	// ownerBook renders the project-book section of the prompt, and is nil when
	// the codebase has no owner. It is a loader rather than a captured string
	// because the book is edited while sessions are open — by the owner itself,
	// on its own turns — so a reload has to re-read it like AGENTS.md.
	ownerBook     func() string
	ownerBookText string
	// ownerRole is the owner's own role prompt, loaded the same way and for the
	// same reason: it carries book/OWNER.md, which the user edits while the
	// owner's conversation is open. Set only in an owner's session.
	ownerRole     func() string
	ownerRoleText string
	// now and gitCtx date the prompt and name its last commit. They are pinned
	// like the inputs above: only a regeneration moves them.
	clock  func() time.Time
	now    time.Time
	gitCtx string
	// system is the base prompt last built from these values: with them, it
	// is what a session freezes (see session.PromptContext).
	system string
}

// NewSources captures what a session was built with. cwd and memStore are kept
// so Reload can re-read the same places the build read.
func NewSources(cwd string, memStore *memory.Store, agentsMD, skillsIndex, memoryIndex string) *Sources {
	return &Sources{
		cwd:         cwd,
		memStore:    memStore,
		agentsMD:    agentsMD,
		skillsIndex: skillsIndex,
		memoryIndex: memoryIndex,
	}
}

// WithClock pins the date and the git section the prompt is built with,
// reading them now; clock is also what a regeneration reads. Nil means
// time.Now.
func (s *Sources) WithClock(clock func() time.Time) *Sources {
	if s == nil {
		return s
	}
	if clock == nil {
		clock = time.Now
	}
	gitCtx := git.Context(s.cwd)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = clock
	s.now = clock()
	s.gitCtx = gitCtx
	return s
}

// Pinned returns the date and the git section the prompt is built with.
func (s *Sources) Pinned() (time.Time, string) {
	if s == nil {
		return time.Time{}, ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.now, s.gitCtx
}

// RecordBuilt records the base prompt just built from these values, as the
// one the session runs with.
func (s *Sources) RecordBuilt(system string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.system = system
}

// Built returns the base prompt last recorded with RecordBuilt.
func (s *Sources) Built() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.system
}

// Freeze returns the values and the prompt as one consistent set, for the
// session to persist. ok is false until a prompt was recorded.
func (s *Sources) Freeze() (pc session.PromptContext, ok bool) {
	if s == nil {
		return pc, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.system == "" {
		return pc, false
	}
	return session.PromptContext{
		Version:     session.PromptContextVersion,
		CWD:         s.cwd,
		Now:         s.now,
		Git:         s.gitCtx,
		AgentsMD:    s.agentsMD,
		SkillsIndex: s.skillsIndex,
		MemoryIndex: s.memoryIndex,
		OwnerRole:   s.ownerRoleText,
		OwnerBook:   s.ownerBookText,
		System:      s.system,
	}, true
}

// Restore replaces the values with a frozen set, so the session goes on with
// the context it had rather than what is on disk now. The loaders stay: a
// regeneration still reads the disk.
func (s *Sources) Restore(pc session.PromptContext) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now, s.gitCtx = pc.Now, pc.Git
	s.agentsMD, s.skillsIndex, s.memoryIndex = pc.AgentsMD, pc.SkillsIndex, pc.MemoryIndex
	if s.ownerRole != nil {
		s.ownerRoleText = pc.OwnerRole
	}
	if s.ownerBook != nil {
		s.ownerBookText = pc.OwnerBook
	}
	s.system = pc.System
}

// WithOwnerBook attaches the project-book loader and reads it once. A nil
// loader leaves the session without a book section, which is the state of
// every codebase that has no owner.
func (s *Sources) WithOwnerBook(load func() string) *Sources {
	if s == nil || load == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ownerBook = load
	s.ownerBookText = load()
	return s
}

// OwnerBook returns the current project-book section ("" when there is none).
func (s *Sources) OwnerBook() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerBookText
}

// WithOwnerRole attaches the owner role loader and reads it once. Only an
// owner's own conversation has one.
func (s *Sources) WithOwnerRole(load func() string) *Sources {
	if s == nil || load == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ownerRole = load
	s.ownerRoleText = load()
	return s
}

// OwnerRole returns the current owner role section ("" when there is none).
func (s *Sources) OwnerRole() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerRoleText
}

// Snapshot returns the current values as one consistent set.
func (s *Sources) Snapshot() (agentsMD, skillsIndex, memoryIndex string) {
	if s == nil {
		return "", "", ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agentsMD, s.skillsIndex, s.memoryIndex
}

// Changed names one input whose content on disk differs from what the session
// holds, along with the fresh content.
type Changed struct {
	Label   string
	Content string
}

// Reload re-reads the three inputs and returns the ones that differ, having
// stored the fresh values.
//
// Nothing changed is the common case — a reload run "just in case" — and it
// must stay free: callers use an empty result to leave the conversation, and
// its cached prompt prefix, completely untouched.
//
// The returned revert function restores the previous values. A caller that
// records the new state and then fails to apply it would report success while
// leaving the session on the old prompt, and the next reload would see no
// difference and do nothing — the change lost for good.
func (s *Sources) Reload() (changed []Changed, revert func()) {
	if s == nil {
		return nil, func() {}
	}
	agentsMD, _ := LoadAgentsMD(s.cwd, "")
	s.mu.RLock()
	loadBook := s.ownerBook
	loadRole := s.ownerRole
	s.mu.RUnlock()
	// Only an owner's own conversation has a role loader, and only it is offered
	// the built-in skills: a reload that dropped them would quietly take back
	// what the session was started with.
	skillsIndex := skill.FormatIndex(skill.Discover(s.cwd, skill.Options{Builtin: loadRole != nil}))
	memoryIndex := ""
	if s.memStore != nil {
		memoryIndex = s.memStore.FormatIndex(s.memStore.List())
	}
	ownerBook := ""
	if loadBook != nil {
		ownerBook = loadBook()
	}
	ownerRole := ""
	if loadRole != nil {
		ownerRole = loadRole()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	prev := struct {
		agentsMD, skillsIndex, memoryIndex, ownerBook, ownerRole, gitCtx, system string
		now                                                                      time.Time
	}{s.agentsMD, s.skillsIndex, s.memoryIndex, s.ownerBookText, s.ownerRoleText, s.gitCtx, s.system, s.now}
	if agentsMD != s.agentsMD {
		changed = append(changed, Changed{Label: "AGENTS.md", Content: agentsMD})
		s.agentsMD = agentsMD
	}
	if skillsIndex != s.skillsIndex {
		changed = append(changed, Changed{Label: "Skills", Content: skillsIndex})
		s.skillsIndex = skillsIndex
	}
	if memoryIndex != s.memoryIndex {
		changed = append(changed, Changed{Label: "Memory index", Content: memoryIndex})
		s.memoryIndex = memoryIndex
	}
	if loadBook != nil && ownerBook != s.ownerBookText {
		changed = append(changed, Changed{Label: "Project book", Content: ownerBook})
		s.ownerBookText = ownerBook
	}
	if loadRole != nil && ownerRole != s.ownerRoleText {
		changed = append(changed, Changed{Label: "Owner preferences", Content: ownerRole})
		s.ownerRoleText = ownerRole
	}
	return changed, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.agentsMD, s.skillsIndex, s.memoryIndex = prev.agentsMD, prev.skillsIndex, prev.memoryIndex
		s.ownerBookText = prev.ownerBook
		s.ownerRoleText = prev.ownerRole
		s.now, s.gitCtx, s.system = prev.now, prev.gitCtx, prev.system
	}
}

// Labels of the Changed entries Regenerate adds to Reload's.
const (
	LabelDate = "Date"
	LabelGit  = "Last commit"
)

// Regenerate is Reload plus a new date and git section: what a compaction or
// Start fresh does, since the session's cached prefix is lost anyway. The
// prompt renders only the day, so a new time on the same day changes nothing
// the model sees. revert undoes all of it.
func (s *Sources) Regenerate() (changed []Changed, revert func()) {
	if s == nil {
		return nil, func() {}
	}
	s.mu.RLock()
	clock := s.clock
	s.mu.RUnlock()
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	gitCtx := git.Context(s.cwd)

	changed, revert = s.Reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sameDay(now, s.now) {
		changed = append(changed, Changed{Label: LabelDate, Content: now.Format("2006-01-02")})
		s.now = now
	}
	if gitCtx != s.gitCtx {
		changed = append(changed, Changed{Label: LabelGit, Content: gitCtx})
		s.gitCtx = gitCtx
	}
	return changed, revert
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
