package tool

import (
	"context"
	"encoding/json"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/sessioncheckpoint"
)

func NewSessionCheckpoint(slot *sessioncheckpoint.Slot) core.Tool {
	return core.Tool{Name: "checkpoint", Label: "Checkpoint", Effect: core.EffectUnknown,
		Description: "Read, replace, or clear this session's ephemeral pre-compaction checkpoint. Use it for temporary task progress that must survive the next compaction; it is not durable memory and must not contain long-term facts.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["read","write","clear"]},"content":{"type":"string","description":"Replacement checkpoint text for write (maximum 16 KiB)."}},"required":["action"]}`),
		Execute: func(_ context.Context, p map[string]any, _ func(core.Result)) (core.Result, error) {
			switch getString(p, "action", "") {
			case "read":
				v, _ := slot.Read()
				if v == "" {
					return core.TextResult("No checkpoint is set."), nil
				}
				return core.TextResult(v), nil
			case "write":
				if err := slot.Write(getString(p, "content", "")); err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				return core.TextResult("checkpoint saved"), nil
			case "clear":
				slot.Clear()
				return core.TextResult("checkpoint cleared"), nil
			}
			return core.ErrorResult("action must be read, write, or clear"), nil
		},
	}
}
