//go:build unix && !linux

package secretaction

func excludeFromDumps([]byte) error { return nil }
