package bus

// NativeContextWindow is the context usage reported by a backend that keeps
// its own context (core.NativeContext, read through the agent), when there is
// one: the session's indicator shows it instead of moa's own estimate.
func NativeContextWindow(agent AgentController) (used, window int, ok bool) {
	n, isNative := agent.(interface {
		NativeContextUsage() (used, window int, ok bool)
	})
	if !isNative {
		return 0, 0, false
	}
	used, window, ok = n.NativeContextUsage()
	return used, window, ok && window > 0
}

func nativeContextPercent(agent AgentController) (int, bool) {
	used, window, ok := NativeContextWindow(agent)
	if !ok {
		return 0, false
	}
	return min(used*100/window, 100), true
}
