package agent

// WorkEntry is what an agent reports, under the lock that admits and consumes
// its queued input (steerMu), as input is accepted or work proceeds. Because
// the same lock serializes both, a caller sees admissions and work in the
// order they happened, and sees the queue as it really is at that moment.
type WorkEntry struct {
	// Admitted is the provenance (Custom) of an input accepted now: a message
	// entering the queue, or a prompt about to start a run (AdmitInput). Nil
	// when the entry accepts nothing new, including the delivery of input
	// queued earlier.
	Admitted map[string]any
	// Work says work proceeds now: a run starts, queued input is delivered, or
	// a run goes on to its next provider request. False for input only queued.
	Work bool
	// Resumed narrows Work to a run going on to its next provider request,
	// which consumes nothing: the most frequent entry by far.
	Resumed bool
	// Queued is the provenance of the input still waiting in the queue after
	// this entry. Set only with Work.
	Queued []map[string]any
}

// WorkGate runs fn under the lock that admits and consumes the agent's input,
// so fn sees no admission or work entry half done. fn must not call back into
// the agent's queue or start a run, and must not wait on anything that does.
func (a *Agent) WorkGate(fn func()) {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	fn()
}

// AdmitInput reports a prompt accepted to start a run, before the run is
// launched, so it is accepted (or refused) with whatever it carries in the
// same step. A queued message is reported by TrySteer instead.
func (a *Agent) AdmitInput(custom map[string]any) error {
	return a.enterWork(WorkEntry{Admitted: custom})
}

// enterWorkLocked reports an entry to OnWork. Callers hold steerMu.
func (a *Agent) enterWorkLocked(e WorkEntry) error {
	if a.config.OnWork == nil || !e.Work && e.Admitted == nil {
		return nil
	}
	if e.Work {
		for _, it := range a.steers.snapshot() {
			if !it.IsBarrier() {
				e.Queued = append(e.Queued, it.Custom)
			}
		}
	}
	return a.config.OnWork(e)
}

// enterWork is enterWorkLocked for a caller that does not hold steerMu.
func (a *Agent) enterWork(e WorkEntry) error {
	if a.config.OnWork == nil {
		return nil
	}
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	return a.enterWorkLocked(e)
}
