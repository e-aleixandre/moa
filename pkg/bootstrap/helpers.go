package bootstrap

import (
	"fmt"

	"github.com/ealeixandre/moa/pkg/core"
)

// CurrentPermissionMode returns the string representation of the current mode.
func (s *Session) CurrentPermissionMode() string {
	if s.Gate == nil {
		return "yolo"
	}
	return string(s.Gate.Mode())
}

// FullModelSpec returns "provider/id" for the given model, or just "id".
func FullModelSpec(model core.Model) string {
	if model.Provider != "" {
		return fmt.Sprintf("%s/%s", model.Provider, model.ID)
	}
	return model.ID
}
