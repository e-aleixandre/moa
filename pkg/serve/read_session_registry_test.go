package serve

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// R01/R02: normal conversations (new and resumed) get read_session and the
// model is offered it; owner conversations do not have it.
func TestReadSessionRegistryNormalHasItOwnerDoesNot(t *testing.T) {
	var firstReq core.Request
	seen := make(chan core.Request, 1)
	env := newRSEnv(t, func(req core.Request) <-chan core.AssistantEvent {
		select {
		case seen <- req:
		default:
		}
		return simpleResponse("ok")
	})
	normal := env.session(env.root)
	if _, ok := normal.infra.toolReg.Get(rsTool); !ok {
		t.Error("normal conversation has no read_session")
	}

	if status, b := env.send(normal.ID, "hello", nil, nil); status != 202 {
		t.Fatalf("send = %d %s", status, b)
	}
	firstReq = rsWait(t, seen)
	if !requestHasTool(firstReq, rsTool) {
		t.Error("the model is not offered read_session")
	}
	rsSettle(t, normal)

	if err := env.mgr.CloseSession(normal.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := env.mgr.ResumeSession(normal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resumed.infra.toolReg.Get(rsTool); !ok {
		t.Error("resumed conversation has no read_session")
	}

	_, ownerSess := ownerWithSession(t, env.mgr, t.TempDir(), "Proj") // other root: an owner cannot be created beside open sessions
	if _, ok := ownerSess.infra.toolReg.Get(SessionsToolName); !ok {
		t.Fatal("control: owner conversation lost its sessions tool")
	}
	if _, ok := ownerSess.infra.toolReg.Get(rsTool); ok {
		t.Error("owner conversation must not have read_session")
	}
}
