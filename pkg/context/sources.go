package agentcontext

import (
	"sync"

	"github.com/e-aleixandre/moa/pkg/memory"
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
	skillsIndex := skill.FormatIndex(skill.Discover(s.cwd))
	memoryIndex := ""
	if s.memStore != nil {
		memoryIndex = s.memStore.FormatIndex(s.memStore.List())
	}
	s.mu.RLock()
	loadBook := s.ownerBook
	s.mu.RUnlock()
	ownerBook := ""
	if loadBook != nil {
		ownerBook = loadBook()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	prev := struct{ agentsMD, skillsIndex, memoryIndex, ownerBook string }{s.agentsMD, s.skillsIndex, s.memoryIndex, s.ownerBookText}
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
	return changed, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.agentsMD, s.skillsIndex, s.memoryIndex = prev.agentsMD, prev.skillsIndex, prev.memoryIndex
		s.ownerBookText = prev.ownerBook
	}
}
