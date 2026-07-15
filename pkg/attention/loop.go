package attention

import (
	"fmt"
	"sort"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
)

// loop.go — the single owner goroutine. All mutable state lives here as locals;
// no other goroutine touches it. Handles bus events (critInbox) and external
// control requests (ctrl) until Close.

// loopState is the actor's private state.
type loopState struct {
	snaps  map[string]*sessionSnapshot // sessionID -> snapshot (only attached)
	items  map[string]*AttentionItem   // itemID -> item (live, incl. resolved-recently)
	order  []string                    // itemID order for stable init/status
	client ClientSink                  // the one active voice client (may be nil)

	// inflightP0 is the id of the P0 currently being escalated; empty if none.
	// A single P0 is "in flight" for retry/escalation at a time (design §2.2).
	inflightP0 string
}

func (s *Service) loop() {
	defer close(s.done)
	defer s.wg.Done()

	st := &loopState{
		snaps: make(map[string]*sessionSnapshot),
		items: make(map[string]*AttentionItem),
	}

	for {
		select {
		case <-s.quit:
			return
		case m := <-s.critInbox:
			s.handleEvent(st, m)
		case c := <-s.ctrl:
			s.handleCtrl(st, c)
		}
	}
}

// -- Control handling -------------------------------------------------------

func (s *Service) handleCtrl(st *loopState, c ctrlMsg) {
	switch c.kind {
	case ctrlAttach:
		s.doAttach(st, c.session)
		if c.reply != nil {
			c.reply <- ctrlReply{}
		}
	case ctrlDetach:
		s.doDetach(st, c.detach)
	case ctrlSetClient:
		s.doSetClient(st, c.setClient)
	case ctrlClearClient:
		if st.client != nil && c.clearClient != nil && st.client.ID() == c.clearClient.ID() {
			st.client = nil
		}
	case ctrlAck:
		s.doAck(st, c.ack)
	case ctrlUpdateMeta:
		s.doUpdateMeta(st, c.meta)
	case ctrlGetStatus:
		reply := ctrlReply{items: st.unresolvedItems()}
		if c.reply != nil {
			c.reply <- reply
		}
	case ctrlInit:
		// Snapshot of all unresolved items for a freshly-connected client.
		reply := ctrlReply{items: st.unresolvedItems()}
		if c.reply != nil {
			c.reply <- reply
		}
	}
}

func (s *Service) doAttach(st *loopState, req *attachReq) {
	if req == nil {
		return
	}
	// Re-attach replaces the snapshot but keeps its live items (a resume).
	snap, ok := st.snaps[req.sessionID]
	if !ok {
		snap = newSessionSnapshot(req.sessionID, req.alias, req.title, req.gen)
		st.snaps[req.sessionID] = snap
	} else {
		snap.gen = req.gen
		snap.alias = req.alias
		snap.title = req.title
	}
	if req.seedState != "" {
		snap.state = req.seedState
	}
	if req.seedState == bus.StateError {
		s.ensureErrorItem(st, snap, req.seedError)
	}
	// Seed pending items (dedup by refID handles overlap with later events).
	for _, p := range req.seedPerm {
		s.ensurePermItem(st, snap, p.refID, p.toolName, p.args)
	}
	for _, a := range req.seedAsk {
		s.ensureAskItem(st, snap, a.refID, a.questions)
	}
	s.notifyRoster(st)
}

func (s *Service) doDetach(st *loopState, sessionID string) {
	snap, ok := st.snaps[sessionID]
	if !ok {
		return
	}
	// Invalidate generation: any later event with the old gen is dropped by
	// handleEvent. Purge the session's items so nothing lingers.
	delete(st.snaps, sessionID)
	var keep []string
	for _, id := range st.order {
		if it, ok := st.items[id]; ok {
			if it.SessionID == sessionID {
				if st.inflightP0 == id {
					st.inflightP0 = ""
				}
				delete(st.items, id)
				continue
			}
			keep = append(keep, id)
		}
	}
	st.order = keep
	_ = snap
	s.notifyRoster(st)
}

func (s *Service) doSetClient(st *loopState, c ClientSink) {
	// Single active client: the newcomer becomes the sink; the previous one is
	// told it's inactive.
	if st.client != nil && c != nil && st.client.ID() != c.ID() {
		st.client.Send(ServerMsg{Type: "inactive", V: ProtocolVersion})
	}
	st.client = c
	// init is authoritative: send the full unresolved set + the session roster.
	if c != nil {
		c.Send(ServerMsg{Type: "init", V: ProtocolVersion,
			Items: st.unresolvedItems(), Sessions: st.roster()})
	}
}

func (s *Service) doUpdateMeta(st *loopState, m *metaUpdate) {
	if m == nil {
		return
	}
	snap, ok := st.snaps[m.sessionID]
	if !ok {
		return
	}
	changed := false
	if m.alias != "" && m.alias != snap.alias {
		snap.alias = m.alias
		changed = true
	}
	if m.title != "" && m.title != snap.title {
		snap.title = m.title
		changed = true
	}
	if changed {
		s.notifyRoster(st)
	}
}

func (s *Service) doAck(st *loopState, itemID string) {
	it, ok := st.items[itemID]
	if !ok {
		return
	}
	// Ack stops escalation but does NOT resolve (design §2.2). Idempotent.
	if it.State == StatePending || it.State == StateAnnounced {
		it.State = StateAcked
	}
	if st.inflightP0 == itemID {
		st.inflightP0 = ""
		s.promoteNextP0(st)
	}
}

// -- Event handling ---------------------------------------------------------

func (s *Service) handleEvent(st *loopState, m inboxMsg) {
	snap, ok := st.snaps[m.sessionID]
	if !ok {
		return // detached
	}
	if snap.gen != m.gen {
		return // stale generation — session was detached/re-attached
	}

	switch e := m.event.(type) {
	case bus.PermissionRequested:
		s.ensurePermItem(st, snap, e.ID, e.ToolName, e.Args)
	case bus.PermissionResolved:
		s.resolveRef(st, snap, snap.pendingPerm, e.ID)
	case bus.AskUserRequested:
		s.ensureAskItem(st, snap, e.ID, e.Questions)
	case bus.AskUserResolved:
		s.resolveRef(st, snap, snap.pendingAsk, e.ID)
	case bus.StateChanged:
		s.handleStateChange(st, snap, bus.SessionState(e.State), e.Error)
	case bus.RunEnded:
		s.handleRunEnded(st, snap, e)
	case bus.RunStarted:
		// Entering a run clears a stale error snapshot; no item.
		snap.lastError = ""
	case bus.GoalEnded:
		s.emitBriefing(st, snap, KindGoalEnded, P1Terminal,
			s.lang.spokenGoalEnded(snap.alias, e.Reason), e.Reason)
	case bus.GoalChanged:
		// A goal that has stalled for >=2 iterations needs the user's eyes.
		if e.Stalled >= 2 {
			sig := fmt.Sprintf("stalled@%d", e.Stalled)
			s.emitBriefing(st, snap, KindGoalStalled, P1Terminal,
				s.lang.spokenGoalStalled(snap.alias, e.Stalled), sig)
		}
	case bus.GoalIterationEnded:
		// Only narrate a verifier-unavailable pause (Err set); routine
		// satisfied/not-satisfied verdicts are too noisy for voice in Phase 2.
		if e.Err != nil {
			s.emitBriefing(st, snap, KindGoalStalled, P1Terminal,
				s.lang.spokenGoalStalled(snap.alias, e.Iteration), "iter-err:"+e.Err.Error())
		}
	case bus.AutoVerifyEnded:
		if !e.AllPass {
			s.emitBriefing(st, snap, KindVerifyFail, P1Terminal,
				s.lang.spokenVerifyFail(snap.alias, e.Summary), e.Summary)
		}
	}
}

func (s *Service) handleStateChange(st *loopState, snap *sessionSnapshot, state bus.SessionState, errMsg string) {
	snap.state = state
	if state == bus.StateError {
		snap.lastError = errMsg
		s.ensureErrorItem(st, snap, errMsg)
	} else {
		// Errors have no external approval ID. A transition out of error is the
		// authoritative resolution signal; retaining the item would make a
		// recovered session announce a stale failure forever.
		s.resolveErrors(st, snap.id)
	}
	s.notifyRoster(st)
}

func (s *Service) handleRunEnded(st *loopState, snap *sessionSnapshot, e bus.RunEnded) {
	// RunEnded{Err} is deduped against a recent StateChanged(error): if we
	// already have an unresolved error item for this session, don't double it.
	if e.Err == nil {
		// Successful run end is a P2 progress briefing (ephemeral, live-only).
		sig := fmt.Sprintf("run:%v:%d", e.HadEdits, len(e.FinalText))
		s.emitBriefing(st, snap, KindRunOK, P2Progress,
			s.lang.spokenRunOK(snap.alias, e.FinalText, e.HadEdits), sig)
		return
	}
	s.ensureErrorItem(st, snap, e.Err.Error())
}

func (s *Service) ensureErrorItem(st *loopState, snap *sessionSnapshot, msg string) {
	if st.hasUnresolvedError(snap.id) {
		return
	}
	s.addItem(st, &AttentionItem{
		ID: s.nextItemID(), Priority: P0Blocking, Kind: KindError,
		SessionID: snap.id, Alias: snap.alias,
		Spoken: s.lang.spokenError(snap.alias, msg),
		State:  StatePending, CreatedAt: time.Now(),
	})
}

// ensurePermItem creates a permission item unless one already exists for refID
// (dedup — seed + event can race). Runs the deterministic risk parser.
func (s *Service) ensurePermItem(st *loopState, snap *sessionSnapshot, refID, toolName string, args map[string]any) {
	if refID == "" {
		return
	}
	if _, exists := snap.pendingPerm[refID]; exists {
		return
	}
	level, flags := assessRisk(toolName, args)
	it := &AttentionItem{
		ID: s.nextItemID(), Priority: P0Blocking, Kind: KindPermission,
		SessionID: snap.id, Alias: snap.alias, RefID: refID,
		Spoken:    s.lang.spokenPermission(snap.alias, toolName, level, flags),
		State:     StatePending,
		CreatedAt: time.Now(),
		RiskLevel: level,
		RiskFlags: flags,
		Verbatim:  commandString(toolName, args),
	}
	snap.pendingPerm[refID] = it.ID
	s.addItem(st, it)
}

// ensureAskItem creates an ask item unless one already exists for refID.
func (s *Service) ensureAskItem(st *loopState, snap *sessionSnapshot, refID string, questions []bus.AskQuestion) {
	if refID == "" {
		return
	}
	if _, exists := snap.pendingAsk[refID]; exists {
		return
	}
	it := &AttentionItem{
		ID: s.nextItemID(), Priority: P0Blocking, Kind: KindAsk,
		SessionID: snap.id, Alias: snap.alias, RefID: refID,
		Spoken: s.lang.spokenAsk(snap.alias, questions),
		State:  StatePending, CreatedAt: time.Now(),
	}
	snap.pendingAsk[refID] = it.ID
	s.addItem(st, it)
}

// resolveRef resolves the item bound to refID (from pendingPerm/pendingAsk),
// marks it resolved, and notifies the client.
func (s *Service) resolveRef(st *loopState, snap *sessionSnapshot, m map[string]string, refID string) {
	itemID, ok := m[refID]
	if !ok {
		return
	}
	delete(m, refID)
	if it, ok := st.items[itemID]; ok {
		it.State = StateResolved
		if st.inflightP0 == itemID {
			st.inflightP0 = ""
			s.promoteNextP0(st)
		}
		s.notifyUpdate(st, it)
		s.notifyRoster(st) // pending counts changed
	}
}

// -- Queue / delivery -------------------------------------------------------

// addItem registers a new item, enforces the memory bound, delivers it to the
// active client, and manages P0 in-flight serialization.
func (s *Service) addItem(st *loopState, it *AttentionItem) {
	st.items[it.ID] = it
	st.order = append(st.order, it.ID)
	st.enforceBound()

	// Deliver as "attention". P0 serialization: if none in flight, this becomes
	// the in-flight one; otherwise it waits its turn but is still delivered
	// (the client can queue speech). We keep it simple: deliver now, track
	// inflight for escalation only.
	if st.inflightP0 == "" && it.Priority == P0Blocking {
		st.inflightP0 = it.ID
	}
	if it.State == StatePending {
		it.State = StateAnnounced
	}
	s.notifyNew(st, it)
	s.notifyRoster(st) // pending counts changed
}

func (s *Service) notifyNew(st *loopState, it *AttentionItem) {
	if st.client == nil {
		return
	}
	c := it.clone()
	if !st.client.Send(ServerMsg{Type: "attention", V: ProtocolVersion, Item: &c}) {
		st.client = nil
	}
}

// emitBriefing narrates an ephemeral progress/terminal note (Phase 2). Unlike
// a P0 item it is NOT stored, NOT queued, NOT resolvable, and NOT replayed on
// reconnect. It is spoken once — and ONLY if a voice client is actually
// listening. This is the "chief of staff tells you how things are going"
// channel; when nobody is on the line, progress simply isn't narrated (the P0
// blocking channel is independent and always tracked). A per-kind novelty
// filter suppresses an identical repeated verdict.
func (s *Service) emitBriefing(st *loopState, snap *sessionSnapshot, kind Kind, prio Priority, spoken, sig string) {
	if st.client == nil {
		return // live-only: no listener, no narration (and nothing buffered)
	}
	if spoken == "" {
		return
	}
	if snap.lastBriefSig != nil && snap.lastBriefSig[kind] == sig {
		return // novelty filter: same verdict already narrated
	}
	if snap.lastBriefSig != nil {
		snap.lastBriefSig[kind] = sig
	}
	b := Briefing{Priority: prio, Kind: kind, SessionID: snap.id, Alias: snap.alias, Spoken: spoken}
	if !st.client.Send(ServerMsg{Type: "briefing", V: ProtocolVersion, Briefing: &b}) {
		st.client = nil
	}
}

func (s *Service) notifyUpdate(st *loopState, it *AttentionItem) {
	if st.client == nil {
		return
	}
	c := it.clone()
	if !st.client.Send(ServerMsg{Type: "item_update", V: ProtocolVersion, Item: &c}) {
		st.client = nil
	}
}

// notifyRoster pushes the current session roster to the client whenever the set
// of sessions or their high-level state changes (attach, detach, state change).
// Cheap and idempotent enough for a single-user host; the client just replaces
// its roster view.
func (s *Service) notifyRoster(st *loopState) {
	if st.client == nil {
		return
	}
	if !st.client.Send(ServerMsg{Type: "roster", V: ProtocolVersion, Sessions: st.roster()}) {
		st.client = nil
	}
}

// promoteNextP0 picks the next unresolved, unacked P0 as the in-flight one.
func (s *Service) promoteNextP0(st *loopState) {
	if st.inflightP0 != "" {
		return
	}
	for _, id := range st.order {
		if it, ok := st.items[id]; ok {
			if it.Priority == P0Blocking && it.State != StateResolved && it.State != StateAcked {
				st.inflightP0 = id
				return
			}
		}
	}
}

// -- loopState helpers ------------------------------------------------------

func (st *loopState) unresolvedItems() []AttentionItem {
	var out []AttentionItem
	for _, id := range st.order {
		if it, ok := st.items[id]; ok && !it.resolved() {
			out = append(out, it.clone())
		}
	}
	return out
}

// roster returns a stable, sorted view of all attached sessions so the voice
// client knows which agents exist and can address orders to them. Sorted by
// session id for deterministic output (testable, stable across rebuilds).
func (st *loopState) roster() []SessionBrief {
	ids := make([]string, 0, len(st.snaps))
	for id := range st.snaps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]SessionBrief, 0, len(ids))
	for _, id := range ids {
		snap := st.snaps[id]
		out = append(out, SessionBrief{
			SessionID:   snap.id,
			Alias:       snap.alias,
			Title:       snap.title,
			State:       string(snap.state),
			PendingAsks: len(snap.pendingAsk),
			PendingPerm: len(snap.pendingPerm),
		})
	}
	return out
}

func (st *loopState) hasUnresolvedError(sessionID string) bool {
	for _, id := range st.order {
		if it, ok := st.items[id]; ok {
			if it.SessionID == sessionID && it.Kind == KindError && !it.resolved() {
				return true
			}
		}
	}
	return false
}

func (s *Service) resolveErrors(st *loopState, sessionID string) {
	for _, id := range st.order {
		it, ok := st.items[id]
		if !ok || it.SessionID != sessionID || it.Kind != KindError || it.resolved() {
			continue
		}
		it.State = StateResolved
		if st.inflightP0 == id {
			st.inflightP0 = ""
			s.promoteNextP0(st)
		}
		s.notifyUpdate(st, it)
	}
}

// enforceBound trims the oldest RESOLVED items when over the live cap, so we
// never drop something the user still needs.
func (st *loopState) enforceBound() {
	if len(st.items) <= maxLiveItems {
		return
	}
	var keep []string
	removed := 0
	overflow := len(st.items) - maxLiveItems
	for _, id := range st.order {
		it, ok := st.items[id]
		if !ok {
			continue
		}
		if removed < overflow && it.resolved() {
			delete(st.items, id)
			removed++
			continue
		}
		keep = append(keep, id)
	}
	st.order = keep
}
