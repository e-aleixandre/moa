package tasks

import (
	"encoding/json"
	"strings"
	"testing"
)

// `to` names the other owner by name or by ID: the composer's @ mention hands
// the model an ID, and the description must say it is accepted.
func TestSendToAcceptsOwnerNameOrID(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	dir := &fakeOwners{owners: []OwnerRef{moa, infra}, allow: map[[2]string]bool{{moa.ID, infra.ID}: true}}
	sender := NewScope(r, moa.SessionID, "").WithOwners(dir, true)

	var schema struct {
		Properties struct {
			To struct {
				Description string `json:"description"`
			} `json:"to"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(NewTool(sender).Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if got, want := schema.Properties.To.Description, "For send: the other owner's name or ID"; got != want {
		t.Errorf("to description = %q, want %q", got, want)
	}

	text, isErr := runTool(t, sender, map[string]any{"action": "send", "to": infra.ID, "title": "by id"})
	if isErr || !strings.Contains(text, infra.Name) {
		t.Fatalf("send by owner ID: err=%v %s", isErr, text)
	}
}
