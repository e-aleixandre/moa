package serve

// Acceptance criteria (Gherkin) for "the owner hears what the user says in a
// session it launched":
//
//	Scenario: the user writes in a session the owner launched
//	  Given a project with an owner and a child session with origin "owner"
//	  When the user sends a message to the child from the web UI
//	  Then the owner receives a report naming the child's id and title
//	  And the report carries the user's text
//
//	Scenario: the user answers a question of that child
//	  Given the child has an open ask_user question (loaded or saved)
//	  When the user answers it
//	  Then the owner is told which question was answered and with what
//
//	Scenario: only the human is relayed
//	  When an agent (the owner, an automation) sends to the child
//	  Or the user writes in a session with origin "user"
//	  Or in the owner's own conversation
//	  Or in a child detached from the owner
//	  Then the owner receives no user-said entry
//
//	Scenario: a burst of messages is one wake-up
//	  When the user sends several messages within the batching window
//	  Then the owner receives them in a single delivery
//
//	Scenario: a long message is abridged
//	  When the user's message exceeds the cap
//	  Then the report keeps its start and end and says how to read it whole

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/owner"
)

// userSaidTexts returns the owner's received batches that carry a user-said
// entry.
func userSaidTexts(sess *ManagedSession) []string {
	var out []string
	for _, text := range ownerReportText(sess) {
		if strings.Contains(text, "status: user_said") {
			out = append(out, text)
		}
	}
	return out
}

func ownerOriginChild(t *testing.T, mgr *Manager, root, title string) *ManagedSession {
	t.Helper()
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Title: title, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

// quietFor asserts nothing user-said reaches the owner after everything that
// could produce it had time to.
func assertNoUserSaid(t *testing.T, ownerSess *ManagedSession, child *ManagedSession) {
	t.Helper()
	child.runtime.WaitQuiescent(context.Background())
	time.Sleep(600 * time.Millisecond)
	if got := userSaidTexts(ownerSess); len(got) != 0 {
		t.Fatalf("the owner was told something it must not hear:\n%s", strings.Join(got, "\n---\n"))
	}
}

func TestOwnerHearsTheUserWritingInItsChild(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerOriginChild(t, mgr, root, "Savia rutinas")

	if _, _, _, err := mgr.SendHuman(child.ID, "usa la hora de Madrid, no UTC", nil, "", "", nil); err != nil {
		t.Fatal(err)
	}
	var got string
	pollUntil(t, 10*time.Second, "the user-said report", func() bool {
		if g := userSaidTexts(ownerSess); len(g) > 0 {
			got = g[0]
			return true
		}
		return false
	})
	for _, want := range []string{child.ID, "Savia rutinas", "the user wrote:", "usa la hora de Madrid, no UTC", "origin: owner"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report lacks %q:\n%s", want, got)
		}
	}
}

func TestOwnerDoesNotHearAgentsWritingInItsChild(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerOriginChild(t, mgr, root, "the child")

	// Manager.Send is what the sessions tool and the automation API use.
	if _, _, _, err := mgr.Send(child.ID, "from an agent", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	assertNoUserSaid(t, ownerSess, child)
}

func TestOwnerDoesNotHearTheUserInASessionItDidNotLaunch(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	mine, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "mine", Origin: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.SendHuman(mine.ID, "my own business", nil, "", "", nil); err != nil {
		t.Fatal(err)
	}
	assertNoUserSaid(t, ownerSess, mine)
}

func TestOwnerDoesNotHearItselfTalkingToTheUser(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	if _, _, _, err := mgr.SendHuman(ownerSess.ID, "hola", nil, "", "", nil); err != nil {
		t.Fatal(err)
	}
	assertNoUserSaid(t, ownerSess, ownerSess)
}

func TestOwnerDoesNotHearADetachedChild(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerOriginChild(t, mgr, root, "detached")
	if _, err := mgr.SetOwnerDetached(child.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.SendHuman(child.ID, "private", nil, "", "", nil); err != nil {
		t.Fatal(err)
	}
	assertNoUserSaid(t, ownerSess, child)
}

func TestAUserBurstIsOneDeliveryToTheOwner(t *testing.T) {
	shortReportWindow(t, 400*time.Millisecond)
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerOriginChild(t, mgr, root, "the child")

	for _, text := range []string{"uno primero", "dos segundo", "tres tercero"} {
		if _, _, _, err := mgr.SendHuman(child.ID, text, nil, "", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	pollUntil(t, 10*time.Second, "the burst", func() bool {
		got = userSaidTexts(ownerSess)
		return len(got) > 0
	})
	time.Sleep(900 * time.Millisecond)
	got = userSaidTexts(ownerSess)
	if len(got) != 1 {
		t.Fatalf("a burst woke the owner %d times:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	for _, want := range []string{"uno primero", "dos segundo", "tres tercero"} {
		if !strings.Contains(got[0], want) {
			t.Fatalf("burst delivery lacks %q:\n%s", want, got[0])
		}
	}
}

func TestALongUserMessageIsAbridgedWithHowToReadIt(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerOriginChild(t, mgr, root, "the child")

	long := "START" + strings.Repeat("x", 6000) + "END"
	if _, _, _, err := mgr.SendHuman(child.ID, long, nil, "", "", nil); err != nil {
		t.Fatal(err)
	}
	var got string
	pollUntil(t, 10*time.Second, "the abridged report", func() bool {
		if g := userSaidTexts(ownerSess); len(g) > 0 {
			got = g[0]
			return true
		}
		return false
	})
	if !strings.Contains(got, "START") || !strings.Contains(got, "END") || len(got) > 3500 {
		t.Fatalf("message not abridged around its ends (len %d)", len(got))
	}
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "action=read, session_id="+child.ID) {
		t.Fatalf("no way to read it whole:\n%s", got)
	}
}

func TestOwnerHearsTheUserAnsweringAChildQuestion(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := "loaded"
		if saved {
			name = "saved"
		}
		t.Run(name, func(t *testing.T) {
			shortReportWindow(t, 50*time.Millisecond)
			e, _, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")
			// The question's own needs_input report is not what is under test.
			if saved {
				f1Unload(t, e, child.ID)
			}
			if code, body := f1PostAsk(t, e, child.ID, qid, "Sí, despliega"); !f1Is2xx(code) {
				t.Fatalf("answer = %d %s", code, body)
			}
			var got string
			pollUntil(t, 10*time.Second, "the answer reaching the owner", func() bool {
				if g := userSaidTexts(ownerSess); len(g) > 0 {
					got = g[0]
					return true
				}
				return false
			})
			for _, want := range []string{child.ID, "the user answered", "ask_id " + qid, f1Question, "Sí, despliega"} {
				if !strings.Contains(got, want) {
					t.Fatalf("report lacks %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestOwnerDoesNotHearAnOwnersAnswerAsTheUsers(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	e, _, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")
	if text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid); isErr {
		t.Fatal(text)
	}
	assertNoUserSaid(t, ownerSess, child)
	_ = e
}

func TestUserSaidEntryRendersAsTheUsersOwnWords(t *testing.T) {
	text := reportsMessage(owner.Owner{}, []owner.Report{
		{SessionID: "c1", Title: "Child", Origin: "owner", Status: reportStatusUserSaid, UserKind: owner.UserSaidMessage, UserText: "hazlo así"},
		{SessionID: "c2", Title: "Other", Origin: "owner", Status: reportStatusUserSaid, UserKind: owner.UserSaidAnswer, UserRef: "q_9", UserQuestion: "Which?", UserText: "A"},
	})
	for _, want := range []string{
		"c1 — Child\n  origin: owner\n  status: user_said",
		"the user wrote: hazlo así",
		"the user answered ask_id q_9 (Which?): A",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "book delta") || strings.Contains(text, "git:") {
		t.Fatalf("a user-said entry must not look like a finished turn:\n%s", text)
	}
}
