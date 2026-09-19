//go:build race

package book

// raceEnabled reports that the race detector instruments this build. The scan
// budget is about the product's cost, not about the detector's overhead (~7x
// measured), so the timing assertion is skipped there rather than inflated to a
// number that would hide a real regression.
const raceEnabled = true
