package dreaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// UndoToolName is the owner's tool to undo a night.
const UndoToolName = "dreaming_undo"

// NewUndoTool lets the owner undo one night of the book's dreaming, by the
// commit the morning news gave. It is registered only in the owner's own
// conversation, and only for an owner that has the dreaming configured.
func NewUndoTool(codebaseDir string) core.Tool {
	return core.Tool{
		Name:  UndoToolName,
		Label: "Undo dreaming",
		Description: "Undo one night of the book's nightly consolidation (\"dreaming\"), by the commit its " +
			"morning news gives. Every sheet goes back to what it was before that night, except one someone " +
			"edited since, which is left as it is and listed. Use it only when the user asks.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"commit":{"type":"string","description":"The commit from the morning news (7+ hex characters)."}},"required":["commit"]}`),
		Effect:     core.EffectWritePath,
		LockKey:    func(map[string]any) string { return "dreaming:" + codebaseDir },
		Execute: func(_ context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			commit, _ := params["commit"].(string)
			commit = strings.TrimSpace(commit)
			if len(commit) < 7 || strings.Trim(commit, "0123456789abcdef") != "" {
				return core.ErrorResult("commit must be the hex id from the morning news"), nil
			}
			rep, err := Store{Codebase: codebaseDir}.Undo(commit, nil)
			if errors.Is(err, ErrBusy) {
				return core.ErrorResult(err.Error()), nil
			}
			if err != nil {
				return core.ErrorResult("could not undo: " + err.Error()), nil
			}
			msg := fmt.Sprintf("Undone: %d sheets restored.", len(rep.Applied))
			if len(rep.Conflicts) > 0 {
				msg += " Left as they are because they changed after that night: " + trimIDs(rep.Conflicts) + "."
			}
			return core.TextResult(msg), nil
		},
	}
}
