package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testOwners() (moa, infra OwnerRef) {
	moa = OwnerRef{ID: "own_moa", Name: "moa", SessionID: "s-moa", ProjectKey: "pmoa", ProjectCWD: "/w/moa"}
	infra = OwnerRef{ID: "own_infra", Name: "Infra", SessionID: "s-infra", ProjectKey: "pinfra", ProjectCWD: "/w/infra"}
	return
}

func ownerActor(o OwnerRef) Actor {
	return Actor{SessionID: o.SessionID, ProjectKey: o.ProjectKey, ProjectCWD: o.ProjectCWD}
}

// leaks reports any identifier of the sender a recipient must never see.
func leaks(s string, o OwnerRef) string {
	for _, id := range []string{o.SessionID, o.ProjectKey, o.ProjectCWD, o.ID} {
		if strings.Contains(s, id) {
			return id
		}
	}
	return ""
}

func mustSend(t *testing.T, r *Repo, from, to OwnerRef, allowed bool) Record {
	t.Helper()
	rec, err := r.SendHandoff(bg, from, to, allowed, AgentInput{Title: "LXC for the demo", Description: "4 GB, Debian",
		Subtasks: []SubtaskInput{{Title: "create"}}})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func noticesTo(ns []Notice, session string) []Notice {
	var out []Notice
	for _, n := range ns {
		if n.RecipientSessionID == session {
			out = append(out, n)
		}
	}
	return out
}

// I2: without permission the task is a request in You; the recipient has
// nothing and hears nothing.
func TestSendWithoutPermissionCreatesRequestOnly(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, false)
	if rec.Place != PlaceYou || rec.RequesterSessionID != moa.SessionID || rec.AssigneeSessionID != "" || rec.Status != StatusPending {
		t.Fatalf("request = %+v", rec)
	}
	h := rec.Handoff
	if h == nil || h.State != HandoffAwaiting || h.FromOwnerID != moa.ID || h.FromOwnerName != "moa" ||
		h.ToOwnerID != infra.ID || h.ToOwnerName != "Infra" || h.Outcome != "" {
		t.Fatalf("handoff = %+v", h)
	}
	if ns := notices(t, r, rec.ID); len(ns) != 0 {
		t.Fatalf("notices before approval: %+v", ns)
	}
	if _, err := r.AgentGet(bg, ownerActor(infra), rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recipient reads an unapproved request: %v", err)
	}
	view, err := r.AgentList(bg, ownerActor(infra))
	if err != nil || len(view.Checklist) != 0 {
		t.Fatalf("recipient checklist = %+v, %v", view.Checklist, err)
	}
	mine, err := r.AgentList(bg, ownerActor(moa))
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Requests) != 0 || len(mine.Sent) != 1 || mine.Sent[0].HandoffState != HandoffAwaiting || mine.Sent[0].To != "Infra" {
		t.Fatalf("sender view: requests %+v sent %+v", mine.Requests, mine.Sent)
	}
	list, err := r.List(bg, Filter{})
	if err != nil || list.Counts.OpenRequests != 1 {
		t.Fatalf("open requests = %d, %v", list.Counts.OpenRequests, err)
	}
}

func TestApproveSendCreatesTaskAndWakesRecipient(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, false)
	sent, err := r.ResolveHandoff(bg, rec.ID, rec.Revision, HandoffActionSend)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Place != PlaceAgent || sent.AssigneeSessionID != infra.SessionID || sent.RequesterSessionID != moa.SessionID ||
		sent.Handoff.State != HandoffSent || sent.ProjectKey != infra.ProjectKey {
		t.Fatalf("approved = %+v / %+v", sent, sent.Handoff)
	}
	ns := notices(t, r, rec.ID)
	wantKinds(t, ns, NoticeHandoff)
	n := ns[0]
	if n.RecipientSessionID != infra.SessionID || n.Deliver != DeliverWake {
		t.Fatalf("notice = %+v", n)
	}
	if !strings.Contains(n.Text, "moa") || !strings.Contains(n.Text, "LXC for the demo") ||
		!strings.Contains(n.Text, "<task_description>\n4 GB, Debian\n</task_description>") {
		t.Fatalf("notice text:\n%s", n.Text)
	}
	if id := leaks(n.Text+n.Title, moa); id != "" {
		t.Fatalf("notice leaks %q:\n%s", id, n.Text)
	}
	view, _ := r.AgentList(bg, ownerActor(infra))
	if len(view.Checklist) != 1 || view.Checklist[0].From != "moa" {
		t.Fatalf("recipient checklist = %+v", view.Checklist)
	}
	if _, err := r.ResolveHandoff(bg, rec.ID, sent.Revision, HandoffActionSend); !errors.Is(err, ErrInvalid) {
		t.Fatalf("resolving twice: %v", err)
	}
	// Stale revision.
	other := mustSend(t, r, moa, infra, false)
	var conflict *ConflictError
	if _, err := r.ResolveHandoff(bg, other.ID, other.Revision+5, HandoffActionSend); !errors.As(err, &conflict) {
		t.Fatalf("stale revision: %v", err)
	}

	// With permission it goes straight there.
	direct := mustSend(t, r, moa, infra, true)
	if direct.Place != PlaceAgent || direct.Handoff.State != HandoffSent {
		t.Fatalf("direct = %+v", direct)
	}
	wantKinds(t, notices(t, r, direct.ID), NoticeHandoff)
}

// D9: declining closes it as Can't do and tells the sender, nobody else.
func TestDeclineClosesCantDoAndNotifiesSender(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, false)
	out, err := r.ResolveHandoff(bg, rec.ID, rec.Revision, HandoffActionDecline)
	if err != nil {
		t.Fatal(err)
	}
	if out.Place != PlaceYou || out.Status != StatusDone || out.Handoff.State != HandoffDeclined ||
		out.Handoff.Outcome != OutcomeCantDo || out.CompletionNote != "Declined by the owner" {
		t.Fatalf("declined = %+v / %+v", out, out.Handoff)
	}
	ns := notices(t, r, rec.ID)
	wantKinds(t, ns, NoticeHandoffDeclined)
	if ns[0].RecipientSessionID != moa.SessionID || ns[0].Deliver != DeliverWake || !strings.Contains(ns[0].Text, "Infra") {
		t.Fatalf("decline notice = %+v", ns[0])
	}
	if _, err := r.ResolveHandoff(bg, rec.ID, out.Revision, HandoffActionSend); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sending a declined request: %v", err)
	}
	mine, _ := r.AgentList(bg, ownerActor(moa))
	if len(mine.Sent) != 1 || mine.Sent[0].Outcome != OutcomeCantDo {
		t.Fatalf("sent = %+v", mine.Sent)
	}
}

// I1: whoever writes a comment never hears of it; the other side does.
func TestCommentNeverNotifiesAuthor(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, true)
	base := len(notices(t, r, rec.ID))

	if _, err := r.AgentComment(bg, ownerActor(infra), rec.ID, "No memory for 4 GB; is 2 GB fine?"); err != nil {
		t.Fatal(err)
	}
	ns := notices(t, r, rec.ID)
	if len(ns) != base+1 || ns[0].Kind != NoticeComment || ns[0].RecipientSessionID != moa.SessionID || ns[0].Deliver != DeliverWake {
		t.Fatalf("after recipient comment: %+v", ns)
	}
	if !strings.Contains(ns[0].Text, "<comment>\nNo memory for 4 GB; is 2 GB fine?\n</comment>") || !strings.Contains(ns[0].Text, "Infra") {
		t.Fatalf("comment notice:\n%s", ns[0].Text)
	}
	if id := leaks(ns[0].Text, infra); id != "" {
		t.Fatalf("comment notice leaks %q", id)
	}

	if _, err := r.AgentComment(bg, ownerActor(moa), rec.ID, "2 GB is fine"); err != nil {
		t.Fatal(err)
	}
	ns = notices(t, r, rec.ID)
	if len(ns) != base+2 || ns[0].RecipientSessionID != infra.SessionID {
		t.Fatalf("after sender comment: %+v", ns)
	}
	if id := leaks(ns[0].Text, moa); id != "" {
		t.Fatalf("comment notice leaks %q", id)
	}

	// An agent's own request: commenting on it tells nobody; the user's
	// comment wakes the agent.
	req, err := r.AgentAsk(bg, actor("s1", "p"), AgentInput{Title: "need the key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AgentComment(bg, actor("s1", "p"), req.ID, "it is for staging"); err != nil {
		t.Fatal(err)
	}
	if ns := notices(t, r, req.ID); len(ns) != 0 {
		t.Fatalf("author's own comment notified: %+v", ns)
	}
	if _, err := r.HumanComment(bg, req.ID, "which vault?"); err != nil {
		t.Fatal(err)
	}
	ns = notices(t, r, req.ID)
	if len(ns) != 1 || ns[0].RecipientSessionID != "s1" || ns[0].Kind != NoticeComment || ns[0].Deliver != DeliverWake {
		t.Fatalf("user comment on a request: %+v", ns)
	}

	thread, err := r.Comments(bg, rec.ID)
	if err != nil || len(thread) != 2 || thread[0].AuthorName != "Infra" || thread[0].AuthorKind != "session" || thread[1].AuthorName != "moa" {
		t.Fatalf("thread = %+v, %v", thread, err)
	}
	got, _ := r.Get(bg, rec.ID)
	if got.CommentCount != 2 {
		t.Fatalf("comment_count = %d", got.CommentCount)
	}

	// D8: no thread on a plain checklist task, and a stranger cannot comment.
	own, _ := r.AgentCreate(bg, actor("s1", "p"), AgentInput{Title: "mine"})
	if _, err := r.AgentComment(bg, actor("s1", "p"), own.ID, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("comment on a checklist task: %v", err)
	}
	if _, err := r.HumanComment(bg, own.ID, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("user comment on a checklist task: %v", err)
	}
	if _, err := r.AgentComment(bg, actor("s-other", "pinfra"), rec.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger comment: %v", err)
	}
	if _, err := r.AgentComment(bg, ownerActor(infra), rec.ID, "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty comment: %v", err)
	}
}

func TestHumanCommentNotifiesBothOwners(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, true)
	base := len(notices(t, r, rec.ID))
	c, err := r.HumanComment(bg, rec.ID, "use pve02")
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthorKind != "human" {
		t.Fatalf("comment = %+v", c)
	}
	ns := notices(t, r, rec.ID)[:len(notices(t, r, rec.ID))-base]
	if len(noticesTo(ns, moa.SessionID)) != 1 || len(noticesTo(ns, infra.SessionID)) != 1 || len(ns) != 2 {
		t.Fatalf("user comment notices = %+v", ns)
	}
	for _, n := range ns {
		if n.Kind != NoticeComment || n.Deliver != DeliverWake || !strings.Contains(n.Text, "The user") {
			t.Fatalf("notice = %+v", n)
		}
	}

	// Before approval the recipient knows nothing: only the sender hears.
	pending := mustSend(t, r, moa, infra, false)
	if _, err := r.HumanComment(bg, pending.ID, "why?"); err != nil {
		t.Fatal(err)
	}
	ns = notices(t, r, pending.ID)
	if len(ns) != 1 || ns[0].RecipientSessionID != moa.SessionID {
		t.Fatalf("comment before approval: %+v", ns)
	}

	// Closed tasks keep their thread and still notify.
	closed, err := r.AgentClose(bg, ownerActor(infra), rec.ID, OutcomeDone, "LXC 112")
	if err != nil {
		t.Fatal(err)
	}
	_ = closed
	before := len(notices(t, r, rec.ID))
	if _, err := r.AgentComment(bg, ownerActor(moa), rec.ID, "thanks"); err != nil {
		t.Fatal(err)
	}
	if got := notices(t, r, rec.ID); len(got) != before+1 || got[0].RecipientSessionID != infra.SessionID {
		t.Fatalf("comment on a closed task: %+v", got[:1])
	}
}

// I5: the recipient closes with a note, always; the sender hears the outcome.
func TestHandoffDoneRequiresNote(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, true)
	for _, outcome := range []string{OutcomeDone, OutcomeCantDo, ""} {
		if _, err := r.AgentClose(bg, ownerActor(infra), rec.ID, outcome, "  "); !errors.Is(err, ErrInvalid) {
			t.Fatalf("close %q without a note: %v", outcome, err)
		}
	}
	if _, err := r.AgentDone(bg, ownerActor(infra), rec.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plain done on a task between owners: %v", err)
	}
	if _, err := r.AgentClose(bg, ownerActor(infra), rec.ID, "maybe", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown outcome: %v", err)
	}
	base := len(notices(t, r, rec.ID))
	if _, err := r.AgentClose(bg, ownerActor(infra), rec.ID, OutcomeDone, "LXC 112, IP 10.0.0.12"); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(bg, rec.ID)
	if got.Status != StatusDone || got.Handoff.Outcome != OutcomeDone || got.CompletionNote != "LXC 112, IP 10.0.0.12" {
		t.Fatalf("closed = %+v / %+v", got, got.Handoff)
	}
	ns := notices(t, r, rec.ID)
	if len(ns) != base+1 || ns[0].Kind != NoticeHandoffClosed || ns[0].RecipientSessionID != moa.SessionID || ns[0].Deliver != DeliverWake {
		t.Fatalf("close notices = %+v", ns)
	}
	if !strings.Contains(ns[0].Text, "<note>\nLXC 112, IP 10.0.0.12\n</note>") {
		t.Fatalf("close text:\n%s", ns[0].Text)
	}
	mine, _ := r.AgentList(bg, ownerActor(moa))
	if len(mine.Sent) != 1 || mine.Sent[0].Status != StatusDone || mine.Sent[0].Outcome != OutcomeDone {
		t.Fatalf("sent = %+v", mine.Sent)
	}
	// Outcome is only for tasks between owners.
	own, _ := r.AgentCreate(bg, actor("s1", "p"), AgentInput{Title: "mine"})
	if _, err := r.AgentClose(bg, actor("s1", "p"), own.ID, OutcomeCantDo, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outcome on a checklist task: %v", err)
	}
	if _, err := r.AgentClose(bg, actor("s1", "p"), own.ID, "", "notes"); err != nil {
		t.Fatalf("done with a note on a checklist task: %v", err)
	}
}

func TestCantDoNotifiesRequesterWithReason(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, true)
	if _, err := r.AgentClose(bg, ownerActor(infra), rec.ID, OutcomeCantDo, "pve01 out of RAM"); err != nil {
		t.Fatal(err)
	}
	ns := notices(t, r, rec.ID)
	if ns[0].Kind != NoticeHandoffClosed || ns[0].RecipientSessionID != moa.SessionID ||
		!strings.Contains(ns[0].Text, "could not") || !strings.Contains(ns[0].Text, "pve01 out of RAM") {
		t.Fatalf("cant_do notice = %+v\n%s", ns[0], ns[0].Text)
	}
	got, _ := r.Get(bg, rec.ID)
	if got.Handoff.Outcome != OutcomeCantDo {
		t.Fatalf("outcome = %q", got.Handoff.Outcome)
	}

	// From the UI: Can't do needs a note; it tells the sender and, as any
	// completion, the assignee.
	ui := mustSend(t, r, moa, infra, true)
	cant := OutcomeCantDo
	if _, err := r.Update(bg, ui.ID, ui.Revision, Patch{Status: ptr(StatusDone), Outcome: &cant}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UI cant_do without a note: %v", err)
	}
	out := mustUpdate(t, r, ui, Patch{Status: ptr(StatusDone), Outcome: &cant, CompletionNote: ptr("no budget")})
	if out.Handoff.Outcome != OutcomeCantDo {
		t.Fatalf("UI outcome = %+v", out.Handoff)
	}
	ns = notices(t, r, ui.ID)
	toMoa, toInfra := noticesTo(ns, moa.SessionID), noticesTo(ns, infra.SessionID)
	if len(toMoa) != 1 || toMoa[0].Kind != NoticeHandoffClosed || toMoa[0].Deliver != DeliverWake || !strings.Contains(toMoa[0].Text, "no budget") {
		t.Fatalf("sender notices = %+v", toMoa)
	}
	if len(toInfra) != 2 || toInfra[0].Kind != NoticeAgentDone {
		t.Fatalf("recipient notices = %+v", toInfra)
	}
	// Done from the UI, no note: allowed, defaults to done.
	ui2 := mustSend(t, r, moa, infra, true)
	out = mustUpdate(t, r, ui2, Patch{Status: ptr(StatusDone)})
	if out.Handoff.Outcome != OutcomeDone || len(noticesTo(notices(t, r, ui2.ID), moa.SessionID)) != 1 {
		t.Fatalf("UI done = %+v", out.Handoff)
	}
	// Outcome is refused on an ordinary task, and a request awaiting
	// permission is resolved with its own actions, not completed.
	plain := note(t, r, "plain")
	if _, err := r.Update(bg, plain.ID, plain.Revision, Patch{Status: ptr(StatusDone), Outcome: &cant, CompletionNote: ptr("x")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outcome on a plain task: %v", err)
	}
	waiting := mustSend(t, r, moa, infra, false)
	if _, err := r.Update(bg, waiting.ID, waiting.Revision, Patch{Status: ptr(StatusDone)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("completing a request awaiting permission: %v", err)
	}
}

// I4: the recipient sees the task, its thread and the sender's name, and
// nothing that identifies the sender's sessions or project.
func TestRecipientSeesOnlyTaskThreadAndSender(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	private, _ := r.AgentCreate(bg, ownerActor(moa), AgentInput{Title: "moa private"})
	rec := mustSend(t, r, moa, infra, true)
	if _, err := r.AgentComment(bg, ownerActor(moa), rec.ID, "context: demo on friday"); err != nil {
		t.Fatal(err)
	}
	got, err := r.AgentGet(bg, ownerActor(infra), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.From != "moa" || len(got.Thread) != 1 || got.Thread[0].Author != "moa" || got.Thread[0].Body != "context: demo on friday" {
		t.Fatalf("recipient view = %+v", got)
	}
	b, _ := json.Marshal(got)
	if id := leaks(string(b), moa); id != "" {
		t.Fatalf("recipient view leaks %q: %s", id, b)
	}
	if _, err := r.AgentGet(bg, ownerActor(infra), private.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recipient reads the sender's checklist: %v", err)
	}
	// The sender sees its own comment as "you".
	mine, _ := r.AgentGet(bg, ownerActor(moa), rec.ID)
	if mine.To != "Infra" || len(mine.Thread) != 1 || mine.Thread[0].Author != "you" {
		t.Fatalf("sender view = %+v", mine)
	}
	scope := NewScope(r, infra.SessionID, "").WithOwners(nil, true)
	res, err := NewTool(scope).Execute(context.Background(), map[string]any{"action": "get", "id": float64(rec.ID)}, nil)
	if err != nil || res.IsError {
		t.Fatalf("get: %+v %v", res, err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "From: moa") || !strings.Contains(text, "context: demo on friday") || leaks(text, moa) != "" {
		t.Fatalf("get text:\n%s", text)
	}
}

func TestSenderCannotEditOrClose(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, true)
	a := ownerActor(moa)
	if _, err := r.AgentGet(bg, a, rec.ID); err != nil {
		t.Fatalf("sender cannot read what it sent: %v", err)
	}
	if _, err := r.AgentUpdate(bg, a, rec.ID, AgentPatch{Title: ptr("changed")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("sender edit: %v", err)
	}
	if _, err := r.AgentDone(bg, a, rec.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("sender done: %v", err)
	}
	if _, err := r.AgentClose(bg, a, rec.ID, OutcomeDone, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("sender close: %v", err)
	}
	waiting := mustSend(t, r, moa, infra, false)
	if _, err := r.AgentUpdate(bg, a, waiting.ID, AgentPatch{Title: ptr("changed")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("sender edits a request awaiting permission: %v", err)
	}
	view, _ := r.AgentList(bg, a)
	if len(view.Checklist) != 0 || len(view.Sent) != 2 {
		t.Fatalf("sender view: checklist %+v sent %+v", view.Checklist, view.Sent)
	}
	got, _ := r.Get(bg, rec.ID)
	if got.Status != StatusPending || got.Title != "LXC for the demo" {
		t.Fatalf("task changed: %+v", got)
	}
}

// fakeOwners is an owner directory over a fixed list.
type fakeOwners struct {
	owners []OwnerRef
	allow  map[[2]string]bool
}

func (f *fakeOwners) OwnerOf(session string) (OwnerRef, bool, error) {
	for _, o := range f.owners {
		if o.SessionID == session {
			return o, true, nil
		}
	}
	return OwnerRef{}, false, nil
}

func (f *fakeOwners) Find(name string) (OwnerRef, bool, error) {
	for _, o := range f.owners {
		if o.ID == name || strings.EqualFold(o.Name, name) {
			return o, true, nil
		}
	}
	return OwnerRef{}, false, nil
}

func (f *fakeOwners) CanSend(from, to string) (bool, error) { return f.allow[[2]string{from, to}], nil }

func runTool(t *testing.T, scope *Scope, params map[string]any) (string, bool) {
	t.Helper()
	res, err := NewTool(scope).Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].Text, res.IsError
}

func toolActions(t *testing.T, scope *Scope) string {
	t.Helper()
	var schema struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(NewTool(scope).Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	return strings.Join(schema.Properties.Action.Enum, ",")
}

// D10: only an owner's own conversation sends.
func TestSendOnlyFromOwner(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	dir := &fakeOwners{owners: []OwnerRef{moa, infra}, allow: map[[2]string]bool{{moa.ID, infra.ID}: true}}
	send := map[string]any{"action": "send", "to": "Infra", "title": "LXC"}

	child := NewScope(r, "s-child", "").WithOwners(dir, false)
	if strings.Contains(toolActions(t, child), "send") {
		t.Fatal("send offered to a non-owner session")
	}
	if text, isErr := runTool(t, child, send); !isErr {
		t.Fatalf("non-owner send accepted: %s", text)
	}
	// Built as an owner session but not an owner's conversation any more.
	impostor := NewScope(r, "s-gone", "").WithOwners(dir, true)
	if text, isErr := runTool(t, impostor, send); !isErr {
		t.Fatalf("send from a session that is no owner accepted: %s", text)
	}
	list, _ := r.List(bg, Filter{IncludeAgents: true})
	if len(list.Tasks) != 0 {
		t.Fatalf("tasks created: %+v", list.Tasks)
	}

	owner := NewScope(r, moa.SessionID, "").WithOwners(dir, true)
	if !strings.Contains(toolActions(t, owner), "send") || !strings.Contains(toolActions(t, owner), "comment") {
		t.Fatalf("owner actions = %s", toolActions(t, owner))
	}
	text, isErr := runTool(t, owner, send)
	if isErr || !strings.Contains(text, "Infra") {
		t.Fatalf("owner send: %s", text)
	}
	// I3: the permission is one-way.
	back := NewScope(r, infra.SessionID, "").WithOwners(dir, true)
	text, isErr = runTool(t, back, map[string]any{"action": "send", "to": "moa", "title": "reverse"})
	if isErr || !strings.Contains(text, "user") {
		t.Fatalf("reverse send: %s", text)
	}
	list, _ = r.List(bg, Filter{IncludeAgents: true})
	places := map[string]Place{}
	for _, rec := range list.Tasks {
		places[rec.Title] = rec.Place
	}
	if places["LXC"] != PlaceAgent || places["reverse"] != PlaceYou {
		t.Fatalf("places = %v", places)
	}
	if text, isErr := runTool(t, owner, map[string]any{"action": "send", "to": "nobody", "title": "x"}); !isErr {
		t.Fatalf("send to an unknown owner: %s", text)
	}
	if text, isErr := runTool(t, owner, map[string]any{"action": "list"}); isErr || !strings.Contains(text, "Sent") || !strings.Contains(text, "LXC") {
		t.Fatalf("list:\n%s", text)
	}
}

func TestSendToSelfRejected(t *testing.T) {
	r := newRepo(t)
	moa, _ := testOwners()
	if _, err := r.SendHandoff(bg, moa, moa, true, AgentInput{Title: "me"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self send: %v", err)
	}
	dir := &fakeOwners{owners: []OwnerRef{moa}}
	owner := NewScope(r, moa.SessionID, "").WithOwners(dir, true)
	if text, isErr := runTool(t, owner, map[string]any{"action": "send", "to": "moa", "title": "me"}); !isErr {
		t.Fatalf("self send through the tool: %s", text)
	}
}

// The tool: comment and done with outcome and note.
func TestToolCommentAndClose(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	rec := mustSend(t, r, moa, infra, true)
	scope := NewScope(r, infra.SessionID, "").WithOwners(&fakeOwners{owners: []OwnerRef{moa, infra}}, true)
	id := float64(rec.ID)
	if text, isErr := runTool(t, scope, map[string]any{"action": "comment", "id": id, "body": "2 GB?"}); isErr {
		t.Fatalf("comment: %s", text)
	}
	if text, isErr := runTool(t, scope, map[string]any{"action": "done", "id": id}); !isErr || !strings.Contains(text, "note") {
		t.Fatalf("done without note: %s", text)
	}
	if text, isErr := runTool(t, scope, map[string]any{"action": "done", "id": id, "outcome": "cant_do", "note": "no RAM"}); isErr {
		t.Fatalf("done cant_do: %s", text)
	}
	got, _ := r.Get(bg, rec.ID)
	if got.Handoff.Outcome != OutcomeCantDo || got.CompletionNote != "no RAM" || got.CommentCount != 1 {
		t.Fatalf("after tool close: %+v / %+v", got, got.Handoff)
	}
}
