package serve

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

const (
	voiceLiveAskTTL        = 10 * time.Minute
	voiceLiveAskCap        = 64
	voiceLiveQuestionLimit = 8 << 10
	voiceLiveAnswerLimit   = 500 * 4
	// The call id is minted by the provider and forwarded by the browser, so
	// it is never trusted for a lookup — only compared with the previous one to
	// group the exchanges of one call. The cap is what stops an arbitrary
	// client string from riding into the transcript.
	voiceLiveCallIDLimit = 128
)

type voiceLiveAsk struct {
	sessionID string
	status    string
	answer    string
	msgID     string
	steerID   string
	callID    string
	question  string
	runGen    uint64
	unsub     func()
}

type voiceLiveAskStore struct {
	mu   sync.Mutex
	asks map[string]*voiceLiveAsk
	cap  int
	ttl  time.Duration
	now  func() time.Time
}

func newVoiceLiveAskStore() *voiceLiveAskStore {
	return &voiceLiveAskStore{asks: make(map[string]*voiceLiveAsk), cap: voiceLiveAskCap, ttl: voiceLiveAskTTL, now: time.Now}
}

func voiceLiveAskHandlers(mgr *Manager) (http.HandlerFunc, http.HandlerFunc) {
	store := newVoiceLiveAskStore()
	post := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var body struct {
			SessionID string `json:"session_id"`
			Question  string `json:"question"`
			CallID    string `json:"call_id"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodySize))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(body.SessionID) == "" || strings.TrimSpace(body.Question) == "" || len(body.Question) > voiceLiveQuestionLimit || len(body.CallID) > voiceLiveCallIDLimit {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		sess, ok := mgr.Get(body.SessionID)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown session"})
			return
		}
		msgID, steerID := core.NewMsgID(), core.NewSteerID()
		id, err := store.create(body.SessionID, msgID, steerID, strings.TrimSpace(body.CallID), strings.TrimSpace(body.Question))
		if errors.Is(err, errVoiceLiveAskPending) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "still waiting for the previous question"})
			return
		}
		if errors.Is(err, errVoiceLiveAskCap) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many pending questions"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "could not ask session"})
			return
		}
		unsub := sess.runtime.Bus.SubscribeAll(func(event any) { store.observe(id, event) })
		store.setUnsub(id, unsub)
		prompt := voiceLiveQuestionPrompt(body.Question)
		action, acceptedID, _, err := mgr.send(body.SessionID, prompt, nil, steerID, msgID, voiceLiveAskCustom(body.CallID, body.Question))
		if err != nil || (action == "send" && acceptedID != msgID) || (action == "steer" && acceptedID != steerID) || (action != "send" && action != "steer") {
			store.fail(id)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "could not ask session"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"ask_id": id})
	}
	get := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ask, ok := store.get(r.URL.Query().Get("ask_id"), r.URL.Query().Get("session_id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ask not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": ask.status, "answer": ask.answer})
	}
	return post, get
}

var errVoiceLiveAskCap = errors.New("voice live ask capacity")
var errVoiceLiveAskPending = errors.New("voice live ask pending")

func (s *voiceLiveAskStore) create(sessionID, msgID, steerID, callID, question string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.asks) >= s.cap {
		return "", errVoiceLiveAskCap
	}
	for _, ask := range s.asks {
		if ask.sessionID == sessionID && ask.status == "pending" {
			return "", errVoiceLiveAskPending
		}
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	id := hex.EncodeToString(bytes)
	s.asks[id] = &voiceLiveAsk{sessionID: sessionID, status: "pending", msgID: msgID, steerID: steerID, callID: callID, question: question}
	time.AfterFunc(s.ttl, func() { s.expire(id) })
	return id, nil
}

func (s *voiceLiveAskStore) setUnsub(id string, unsub func()) {
	s.mu.Lock()
	ask := s.asks[id]
	if ask != nil {
		ask.unsub = unsub
	}
	settled := ask == nil || ask.status != "pending"
	s.mu.Unlock()
	if settled && unsub != nil {
		unsub()
	}
}

func (s *voiceLiveAskStore) answer(id, answer string) {
	s.settle(id, "answered", truncateUTF8(answer, voiceLiveAnswerLimit))
}
func (s *voiceLiveAskStore) fail(id string) { s.settle(id, "failed", "") }

// observe binds only the event that announces this exact accepted prompt to a
// run. A terminal event from an already-running or later run must never answer
// a voice question whose queue item has not yet been delivered.
func (s *voiceLiveAskStore) observe(id string, event any) {
	s.mu.Lock()
	ask := s.asks[id]
	if ask == nil || ask.status != "pending" {
		s.mu.Unlock()
		return
	}
	switch event := event.(type) {
	case bus.UserMessageAppended:
		// A prompt carrying transcript provenance is appended by the agent
		// under an ID it mints itself, so the pre-minted msgID only identifies
		// the plain path. The announced envelope identifies this question on
		// the other one, and a session holds at most one pending question.
		if event.MsgID == ask.msgID || ask.announces(event.Custom) {
			ask.runGen = event.RunGen
		}
	case bus.Steered:
		if event.ID == ask.steerID {
			ask.runGen = event.RunGen
		}
	case bus.RunEnded:
		if ask.runGen == 0 || event.RunGen != ask.runGen {
			s.mu.Unlock()
			return
		}
		answer := event.FinalText
		s.mu.Unlock()
		if strings.TrimSpace(answer) == "" {
			s.fail(id)
		} else {
			s.answer(id, answer)
		}
		return
	}
	s.mu.Unlock()
}
func (s *voiceLiveAskStore) settle(id, status, answer string) {
	s.mu.Lock()
	ask := s.asks[id]
	if ask == nil || ask.status != "pending" {
		s.mu.Unlock()
		return
	}
	ask.status, ask.answer = status, answer
	unsub := ask.unsub
	ask.unsub = nil
	s.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// announces reports whether an announced user message is THIS question: the
// envelope the transcript block is built from is also what identifies the
// prompt on the wire, so no second marker is needed to bind the run.
func (a *voiceLiveAsk) announces(custom map[string]any) bool {
	if source, _ := custom["source"].(string); source != "voice_call" {
		return false
	}
	callID, _ := custom["call_id"].(string)
	question, _ := custom["question"].(string)
	return callID == a.callID && question == a.question
}

// voiceLiveAskCustom is the provenance the transcript keeps for a question a
// voice delegate asked this session. The question travels raw because the
// message the session receives is the prompt scaffolding around it, and a
// conversation reopened days later has to show what was asked, not how it was
// wrapped. The call id groups the exchanges of a single call and nothing else.
func voiceLiveAskCustom(callID, question string) map[string]any {
	return map[string]any{
		"source":   "voice_call",
		"call_id":  strings.TrimSpace(callID),
		"question": strings.TrimSpace(question),
	}
}

func voiceLiveQuestionPrompt(question string) string {
	question = strings.ReplaceAll(question, "</voice_question>", "[/voice_question]")
	question = strings.ReplaceAll(question, "<voice_question>", "[voice_question]")
	return "<voice_question>\n" + question + "\n</voice_question>\n\n" +
		"A voice delegate is talking with the owner right now about this conversation.\n" +
		"Answer in at most 3 sentences, plain text, no formatting.\n" +
		"Do not use tools that have any effect: no sessions send/new/answer, no writes to the book, no code edits. Only answer. If you do not know, say so.\n" +
		"If the question conflicts with a decision in the book, say which one."
}
func (s *voiceLiveAskStore) expire(id string) {
	s.mu.Lock()
	ask := s.asks[id]
	if ask == nil {
		s.mu.Unlock()
		return
	}
	delete(s.asks, id)
	unsub := ask.unsub
	s.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}
func (s *voiceLiveAskStore) get(id, sessionID string) (voiceLiveAsk, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ask, ok := s.asks[id]
	if !ok || ask.sessionID != sessionID {
		return voiceLiveAsk{}, false
	}
	return *ask, true
}
