package tool

import (
	"encoding/json"
	"strings"
)

// patchDisplayMaxBytes bounds the diff text one apply_patch result carries in
// total. The record is persisted with the message and replayed to every client
// on reconnect, so it has to stay far below the 64 KiB history string bound
// even after JSON escaping. Counts stay exact past the cut; only diff text is
// dropped, and the file says so.
const patchDisplayMaxBytes = 32 << 10

// PatchFileDisplay is what the UI shows for one file apply_patch wrote. It is
// derived from the before/after contents the tool actually applied, so unlike
// the patch text it reflects tolerant matching and normalised line endings.
type PatchFileDisplay struct {
	Path      string `json:"path"`
	MovePath  string `json:"move_path,omitempty"`
	Op        string `json:"op"` // A, M, D or R
	Add       int    `json:"add"`
	Del       int    `json:"del"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// HasDiff marks a summary (see SummarizePatchFiles) that dropped Diff but
	// can serve it on request.
	HasDiff bool `json:"has_diff,omitempty"`
}

func buildPatchDisplay(staged []stagedChange) []PatchFileDisplay {
	files := make([]PatchFileDisplay, 0, len(staged))
	budget := patchDisplayMaxBytes
	for _, s := range staged {
		f := PatchFileDisplay{Path: s.display}
		switch s.action {
		case actionAdd:
			f.Op = "A"
		case actionDelete:
			f.Op = "D"
		case actionMove:
			f.Op = "R"
			f.MovePath = s.moveName
		default:
			f.Op = "M"
		}
		oldLines, newLines := displayLines(s.before), displayLines(s.content)
		if s.before != "" && s.content != "" && strings.HasSuffix(s.before, "\n") != strings.HasSuffix(s.content, "\n") {
			// One side lost or gained its final newline: keep the phantom last
			// row so that change is visible instead of normalised away.
			oldLines, newLines = splitLines(s.before), splitLines(s.content)
		}
		diff := unifiedDiffLines(oldLines, newLines, 3)
		f.Add, f.Del = diffChangeCounts(diff)
		switch {
		case len(diff) <= budget:
			f.Diff = diff
			budget -= len(diff)
		case budget > 0:
			f.Diff = cutAtLine(diff, budget)
			f.Truncated = true
			budget = 0
		default:
			f.Truncated = true
		}
		files = append(files, f)
	}
	return files
}

func cutAtLine(text string, max int) string {
	cut := strings.LastIndexByte(text[:max], '\n')
	if cut < 0 {
		return ""
	}
	return text[:cut]
}

// diffChangeCounts counts added and removed rows of a unifiedDiff, whose rows
// are "%4d <marker><text>".
func diffChangeCounts(diff string) (add, del int) {
	for _, line := range strings.Split(diff, "\n") {
		i := 0
		for i < len(line) && line[i] == ' ' {
			i++
		}
		j := i
		for j < len(line) && line[j] >= '0' && line[j] <= '9' {
			j++
		}
		if j == i || j+1 >= len(line) || line[j] != ' ' {
			continue
		}
		switch line[j+1] {
		case '+':
			add++
		case '-':
			del++
		}
	}
	return add, del
}

// displayLines splits file text into rows, dropping the empty row a final
// newline leaves behind: "a\n" is one line, not two.
func displayLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// maxPatchSummaryFiles bounds how many files a transcript item lists.
const maxPatchSummaryFiles = 100

// PatchMore is what a summary left out past its file cap, so the card can still
// state the real totals.
type PatchMore struct {
	Files int `json:"files"`
	Add   int `json:"add"`
	Del   int `json:"del"`
}

// SummarizePatchFiles turns a recorded apply_patch result (Result.Custom value,
// either typed or as generic JSON after a reload) into per-file rows without
// diff text, which a closed card shows. HasDiff says the diff can be fetched.
// Past maxPatchSummaryFiles the rest is only counted (more). Nil when the value
// is not a record.
func SummarizePatchFiles(recorded any) (files []PatchFileDisplay, more *PatchMore) {
	if recorded == nil {
		return nil, nil
	}
	raw, err := json.Marshal(recorded)
	if err != nil {
		return nil, nil
	}
	if json.Unmarshal(raw, &files) != nil {
		return nil, nil
	}
	if len(files) > maxPatchSummaryFiles {
		more = &PatchMore{Files: len(files) - maxPatchSummaryFiles}
		for _, f := range files[maxPatchSummaryFiles:] {
			more.Add += f.Add
			more.Del += f.Del
		}
		files = files[:maxPatchSummaryFiles]
	}
	for i := range files {
		files[i].HasDiff = files[i].Diff != ""
		files[i].Diff = ""
	}
	return files, more
}
