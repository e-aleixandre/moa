package serve

import "github.com/e-aleixandre/moa/pkg/session"

// promptContextSnapshot is the persister's view of the session's frozen
// prompt (see session.PromptContext): the prompt the session runs with and
// the inputs it was built from. Nil until the session built or restored one.
func (s *ManagedSession) promptContextSnapshot() *session.PromptContext {
	pc, ok := s.infra.promptSources.Freeze()
	if !ok {
		return nil
	}
	return &pc
}
