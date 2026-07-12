package serve

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/ops"
)

const (
	pulseOperationTargetLimit = 256
)

const pulseOperationDirectedInstruction = "directed_instruction"

const pulseOperationPermissionDecision = "permission_decision"

var (
	errPulseOperationUnavailable = errors.New("Pulse operations unavailable")
	errPulseOperationReviewStale = errors.New("operation review is no longer current")
)

type pulseOperationPrepareBody struct {
	Kind     string `json:"kind"`
	Target   string `json:"target"`
	Text     string `json:"text"`
	Decision string `json:"decision"`

	textProvided     bool
	decisionProvided bool
}

type pulseOperationPrepareWire struct {
	Kind     string  `json:"kind"`
	Target   string  `json:"target"`
	Text     *string `json:"text"`
	Decision *string `json:"decision"`
}

type pulseOperationReview struct {
	Target      opsInstructionTarget `json:"target"`
	Text        string               `json:"text,omitempty"`
	Action      string               `json:"action"`
	Risk        string               `json:"risk"`
	Consequence string               `json:"consequence"`
	Tool        string               `json:"tool,omitempty"`
	Decision    string               `json:"decision,omitempty"`
	Scope       string               `json:"scope,omitempty"`
}

type pulseOperationReceipt struct {
	OperationID string    `json:"operation_id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Action      string    `json:"action,omitempty"`
	Delivery    string    `json:"delivery,omitempty"`
	Observation string    `json:"observation"`
	Completion  string    `json:"completion,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	At          time.Time `json:"at"`
}

type pulseOperationResponse struct {
	OperationID string                 `json:"operation_id"`
	Kind        string                 `json:"kind"`
	Status      string                 `json:"status"`
	ExpiresAt   time.Time              `json:"expires_at,omitempty"`
	Review      *pulseOperationReview  `json:"review,omitempty"`
	Receipt     *pulseOperationReceipt `json:"receipt,omitempty"`
}

func requirePulseOperationDevice(w http.ResponseWriter, r *http.Request) (authIdentity, bool) {
	if !deviceTransportAllowed(r) {
		rejectInsecureDeviceTransport(w)
		return authIdentity{}, false
	}
	identity, ok := requestAuthIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return authIdentity{}, false
	}
	if identity.Kind != "device" || identity.DeviceID == "" {
		http.Error(w, "Pulse operations require paired device authentication", http.StatusForbidden)
		return authIdentity{}, false
	}
	if _, ok := requestDeviceStore(r); !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device authentication unavailable"})
		return authIdentity{}, false
	}
	return identity, true
}

func handlePulseOperationPrepare(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseOperationDevice(w, r)
		if !ok {
			return
		}
		if !validPulseOperationQuery(w, r) {
			return
		}
		body, ok := decodePulseOperationPrepareBody(w, r)
		if !ok {
			return
		}
		response, candidates, err := mgr.preparePulseOperation(identity.DeviceID, body)
		switch {
		case errors.Is(err, errPulseOperationAmbiguous):
			writeJSON(w, http.StatusConflict, struct {
				Candidates []opsInstructionTarget `json:"candidates"`
			}{Candidates: candidates})
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, ErrInstructionPermission), errors.Is(err, errPulseOperationReviewStale), errors.Is(err, bus.ErrPermissionDecisionSnapshotUnavailable):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "operation cannot be reviewed in the target's current state"})
		case errors.Is(err, errOperationAdmission):
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Pulse operation capacity reached; retry after existing reviews or receipts expire"})
		case errors.Is(err, errPulseOperationUnavailable), errors.Is(err, errPulseInstructionLedger), errors.Is(err, errOperationStoreUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Pulse operations temporarily unavailable"})
		case err != nil:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusCreated, response)
		}
	}
}

func decodePulseOperationPrepareBody(w http.ResponseWriter, r *http.Request) (pulseOperationPrepareBody, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return pulseOperationPrepareBody{}, false
	}
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, instructionBodyLimit))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return pulseOperationPrepareBody{}, false
	}
	if !utf8.Valid(bodyBytes) {
		http.Error(w, "request body must be valid UTF-8", http.StatusBadRequest)
		return pulseOperationPrepareBody{}, false
	}
	trimmed := strings.TrimSpace(string(bodyBytes))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return pulseOperationPrepareBody{}, false
	}
	var wire pulseOperationPrepareWire
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		if strings.Contains(err.Error(), `unknown field "feedback"`) {
			http.Error(w, "feedback is not accepted for Pulse permission decisions", http.StatusBadRequest)
			return pulseOperationPrepareBody{}, false
		}
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return pulseOperationPrepareBody{}, false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return pulseOperationPrepareBody{}, false
	}
	body := pulseOperationPrepareBody{Kind: wire.Kind, Target: wire.Target}
	if wire.Text != nil {
		body.Text = *wire.Text
		body.textProvided = true
	}
	if wire.Decision != nil {
		body.Decision = *wire.Decision
		body.decisionProvided = true
	}
	return body, true
}

func handlePulseOperationConfirm(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseOperationDevice(w, r)
		if !ok {
			return
		}
		if !validPulseOperationQuery(w, r) {
			return
		}
		var body struct{}
		if !decodeInstructionBody(w, r, &body) {
			return
		}
		id := r.PathValue("id")
		if !validPulseOperationID(id) {
			http.Error(w, "invalid operation id", http.StatusBadRequest)
			return
		}
		devices, _ := requestDeviceStore(r)
		receipt, err := mgr.confirmPulseOperation(r.Context(), devices, identity.DeviceID, id)
		switch {
		case errors.Is(err, errOperationNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, errOperationDeviceMismatch):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			http.Error(w, "confirmation still in progress", http.StatusRequestTimeout)
		case errors.Is(err, errPulseOperationUnavailable), errors.Is(err, errOperationStoreUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Pulse operations temporarily unavailable"})
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to confirm Pulse operation"})
		default:
			writeJSON(w, http.StatusOK, pulseOperationResponse{OperationID: id, Kind: receipt.Kind, Status: "receipt", Receipt: &receipt})
		}
	}
}

func handlePulseOperationGet(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseOperationDevice(w, r)
		if !ok {
			return
		}
		if !validPulseOperationQuery(w, r) {
			return
		}
		id := r.PathValue("id")
		if !validPulseOperationID(id) {
			http.Error(w, "invalid operation id", http.StatusBadRequest)
			return
		}
		response, err := mgr.pulseOperationStatus(identity.DeviceID, id)
		switch {
		case errors.Is(err, errOperationNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, errOperationDeviceMismatch):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, errPulseOperationUnavailable), errors.Is(err, errOperationStoreUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Pulse operations temporarily unavailable"})
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to read Pulse operation"})
		default:
			writeJSON(w, http.StatusOK, response)
		}
	}
}

func validPulseOperationQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Pulse operation endpoints do not accept query parameters"})
		return false
	}
	return true
}

var errPulseOperationAmbiguous = errors.New("Pulse operation target is ambiguous")

func (m *Manager) preparePulseOperation(deviceID string, body pulseOperationPrepareBody) (pulseOperationResponse, []opsInstructionTarget, error) {
	if m.pulseOperations == nil {
		return pulseOperationResponse{}, nil, errPulseOperationUnavailable
	}
	body.Kind = strings.TrimSpace(body.Kind)
	switch body.Kind {
	case pulseOperationDirectedInstruction:
		if body.decisionProvided {
			return pulseOperationResponse{}, nil, errors.New("invalid directed instruction operation")
		}
		if !m.instructionLedgerAvailable() {
			return pulseOperationResponse{}, nil, errPulseInstructionLedger
		}
		return m.preparePulseDirectedInstruction(deviceID, body.Target, body.Text)
	case pulseOperationPermissionDecision:
		if body.textProvided || !body.decisionProvided {
			return pulseOperationResponse{}, nil, errors.New("invalid permission decision operation")
		}
		return m.preparePulsePermissionDecision(deviceID, body.Target, body.Decision)
	default:
		return pulseOperationResponse{}, nil, errors.New("unsupported Pulse operation kind")
	}
}

func (m *Manager) preparePulseDirectedInstruction(deviceID, target, text string) (pulseOperationResponse, []opsInstructionTarget, error) {
	target = strings.TrimSpace(target)
	text = strings.TrimSpace(text)
	if !validPulseReference(target, pulseOperationTargetLimit) {
		return pulseOperationResponse{}, nil, errors.New("target must be non-empty and no more than 256 safe runes")
	}
	if !validPulseInstructionText(text) {
		return pulseOperationResponse{}, nil, errors.New("text must be non-empty and no more than 1024 runes")
	}
	if m.ops == nil {
		return pulseOperationResponse{}, nil, errPulseOperationUnavailable
	}
	resolution := m.ops.Resolve(target)
	switch len(resolution.Candidates) {
	case 0:
		return pulseOperationResponse{}, nil, ErrNotFound
	case 1:
	default:
		candidates := make([]opsInstructionTarget, len(resolution.Candidates))
		for i, candidate := range resolution.Candidates {
			candidates[i] = safeOpsInstructionTarget(candidate)
		}
		return pulseOperationResponse{}, candidates, errPulseOperationAmbiguous
	}
	candidate := resolution.Candidates[0]
	if candidate.Kind != ops.TargetSession || candidate.ID == "" {
		return pulseOperationResponse{}, nil, ErrNotFound
	}
	session, ok := m.Get(candidate.ID)
	if !ok {
		return pulseOperationResponse{}, nil, ErrNotFound
	}
	expectedAction, err := instructionActionForState(session.runtime.State.Current())
	if err != nil {
		return pulseOperationResponse{}, nil, err
	}
	digest, err := m.pulseOperations.digest(
		pulseOperationDirectedInstruction,
		candidate.ID,
		candidate.Title,
		candidate.CanonicalCWD,
		text,
		expectedAction,
	)
	if err != nil {
		return pulseOperationResponse{}, nil, err
	}
	id, err := newPulseOperationID()
	if err != nil {
		return pulseOperationResponse{}, nil, errPulseOperationUnavailable
	}
	operation := durableOperation{
		ID:             id,
		DeviceID:       deviceID,
		Kind:           pulseOperationDirectedInstruction,
		PayloadDigest:  digest,
		Target:         safeOpsInstructionTarget(candidate),
		Text:           text,
		ExpectedAction: expectedAction,
	}
	if err := m.pulseOperations.create(operation); err != nil {
		return pulseOperationResponse{}, nil, err
	}
	stored, err := m.pulseOperations.get(id, deviceID)
	if err != nil {
		return pulseOperationResponse{}, nil, err
	}
	return pulseOperationResponse{OperationID: stored.ID, Kind: stored.Kind, Status: "pending_confirmation", ExpiresAt: stored.ExpiresAt, Review: directedInstructionReview(stored)}, nil, nil
}

func (m *Manager) preparePulsePermissionDecision(deviceID, target, decision string) (pulseOperationResponse, []opsInstructionTarget, error) {
	target = strings.TrimSpace(target)
	decision = strings.TrimSpace(decision)
	if !validPulseReference(target, pulseOperationTargetLimit) {
		return pulseOperationResponse{}, nil, errors.New("target must be non-empty and no more than 256 safe runes")
	}
	if decision != "approve_once" && decision != "deny" {
		return pulseOperationResponse{}, nil, errors.New("decision must be approve_once or deny")
	}
	if m.ops == nil {
		return pulseOperationResponse{}, nil, errPulseOperationUnavailable
	}
	resolution := m.ops.Resolve(target)
	switch len(resolution.Candidates) {
	case 0:
		return pulseOperationResponse{}, nil, ErrNotFound
	case 1:
	default:
		candidates := make([]opsInstructionTarget, len(resolution.Candidates))
		for i, candidate := range resolution.Candidates {
			candidates[i] = safeOpsInstructionTarget(candidate)
		}
		return pulseOperationResponse{}, candidates, errPulseOperationAmbiguous
	}
	candidate := resolution.Candidates[0]
	if candidate.Kind != ops.TargetSession || candidate.ID == "" {
		return pulseOperationResponse{}, nil, ErrNotFound
	}
	session, ok := m.Get(candidate.ID)
	if !ok {
		return pulseOperationResponse{}, nil, ErrNotFound
	}
	snapshot, err := bus.QueryTyped[bus.GetPermissionDecisionSnapshot, bus.PermissionDecisionSnapshot](session.runtime.Bus, bus.GetPermissionDecisionSnapshot{SessionID: candidate.ID})
	if err != nil {
		return pulseOperationResponse{}, nil, bus.ErrPermissionDecisionSnapshotUnavailable
	}
	if !validPulsePermissionTool(snapshot.ToolName) {
		return pulseOperationResponse{}, nil, errPulseOperationReviewStale
	}
	digest, err := m.permissionDecisionDigest(candidate.ID, snapshot, decision)
	if err != nil {
		return pulseOperationResponse{}, nil, err
	}
	id, err := newPulseOperationID()
	if err != nil {
		return pulseOperationResponse{}, nil, errPulseOperationUnavailable
	}
	operation := durableOperation{
		ID:                           id,
		DeviceID:                     deviceID,
		Kind:                         pulseOperationPermissionDecision,
		PayloadDigest:                digest,
		Target:                       opsInstructionTarget{ID: candidate.ID, Title: redactPulseReviewText(candidate.Title, 120)},
		PermissionID:                 snapshot.PermissionID,
		PermissionRunGen:             snapshot.RunGen,
		PermissionTool:               snapshot.ToolName,
		PermissionAllowPatternDigest: snapshot.AllowPatternDigest,
		PermissionArgsDigest:         snapshot.ArgsDigest,
		PermissionDecision:           decision,
	}
	if err := m.pulseOperations.create(operation); err != nil {
		return pulseOperationResponse{}, nil, err
	}
	stored, err := m.pulseOperations.get(id, deviceID)
	if err != nil {
		return pulseOperationResponse{}, nil, err
	}
	return pulseOperationResponse{OperationID: stored.ID, Kind: stored.Kind, Status: "pending_confirmation", ExpiresAt: stored.ExpiresAt, Review: permissionDecisionReview(stored)}, nil, nil
}

func (m *Manager) permissionDecisionDigest(sessionID string, snapshot bus.PermissionDecisionSnapshot, decision string) (string, error) {
	return m.pulseOperations.digest(
		pulseOperationPermissionDecision,
		sessionID,
		snapshot.PermissionID,
		snapshot.ToolName,
		snapshot.AllowPatternDigest,
		snapshot.ArgsDigest,
		stringifyRunGen(snapshot.RunGen),
		decision,
	)
}

func stringifyRunGen(value uint64) string {
	return strconv.FormatUint(value, 10)
}

func instructionActionForState(state bus.SessionState) (string, error) {
	switch state {
	case bus.StateIdle, bus.StateError:
		return "send", nil
	case bus.StateRunning:
		return "steer", nil
	case bus.StatePermission:
		return "", ErrInstructionPermission
	default:
		return "", errPulseOperationReviewStale
	}
}

func directedInstructionReview(operation durableOperation) *pulseOperationReview {
	consequence := "Moa will deliver this instruction to the selected session. Delivery does not mean the requested work is complete."
	if operation.ExpectedAction == "steer" {
		consequence = "Moa will steer the selected running session with this instruction. Delivery does not mean the requested work is complete."
	}
	return &pulseOperationReview{
		Target:      operation.Target,
		Text:        operation.Text,
		Action:      operation.ExpectedAction,
		Risk:        "This changes the selected agent's next work.",
		Consequence: consequence,
	}
}

func permissionDecisionReview(operation durableOperation) *pulseOperationReview {
	return &pulseOperationReview{
		Target:      operation.Target,
		Action:      operation.PermissionDecision,
		Tool:        redactPulseReviewText(operation.PermissionTool, 80),
		Decision:    operation.PermissionDecision,
		Scope:       "one-time permission request",
		Risk:        "This resolves only the reviewed pending tool permission once.",
		Consequence: "Moa will apply this decision only if the exact pending request is still current.",
	}
}

var pulseReviewSecretValue = regexp.MustCompile(`(?i)(["']?(?:secret|token|password|authorization|api[_-]?key|access[_-]?key|private[_-]?key|key)["']?\s*[:=]\s*["']?)([^\s,;\]\}\)"']+)`)
var pulseReviewAuthorizationValue = regexp.MustCompile(`(?i)(\bauthorization\b\s*[:=]\s*(?:(?:bearer|basic|token)\s+)?)([^\s,;\]\}\)"']+)`)
var pulseReviewCredentialValue = regexp.MustCompile(`(?i)(\b(?:authorization|bearer|basic|token)\b\s+)([^\s,;\]\}\)"']+)`)
var pulseReviewSensitiveWordValue = regexp.MustCompile(`(?i)(\b(?:secret|token|password|authorization|(?:api|access|private)[_-]?key|key)\b\s+)([^\s,;\]\}\)"']+)`)

// redactPulseReviewText is intentionally conservative: a review never needs
// raw tool arguments, and any incidental sensitive key/value notation is
// replaced before it can leave the typed operation boundary.
func redactPulseReviewText(value string, limit int) string {
	if !utf8.ValidString(value) {
		return "[unavailable]"
	}
	value = pulseReviewAuthorizationValue.ReplaceAllString(value, "$1[redacted]")
	value = pulseReviewSecretValue.ReplaceAllString(value, "$1[redacted]")
	value = pulseReviewCredentialValue.ReplaceAllString(value, "$1[redacted]")
	value = pulseReviewSensitiveWordValue.ReplaceAllString(value, "$1[redacted]")
	var out strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			out.WriteByte(' ')
			continue
		}
		out.WriteRune(r)
	}
	value = strings.TrimSpace(out.String())
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}

func (m *Manager) confirmPulseOperation(ctx context.Context, devices *deviceStore, deviceID, id string) (pulseOperationReceipt, error) {
	if m.pulseOperations == nil {
		return pulseOperationReceipt{}, errPulseOperationUnavailable
	}
	if devices == nil {
		return pulseOperationReceipt{}, errPulseOperationUnavailable
	}
	start, err := m.pulseOperations.beginConfirm(id, deviceID)
	if err != nil {
		return pulseOperationReceipt{}, err
	}
	if start.Receipt != nil {
		return *start.Receipt, nil
	}
	if start.Wait != nil {
		select {
		case <-start.Wait:
			return m.pulseOperations.finalizedReceipt(id, deviceID)
		case <-ctx.Done():
			return pulseOperationReceipt{}, ctx.Err()
		}
	}
	if !start.Execute {
		return pulseOperationReceipt{}, errOperationStoreUnavailable
	}
	if start.Operation.Kind == pulseOperationDirectedInstruction && !m.instructionLedgerAvailable() {
		if start.Recover && start.Operation.Attempted {
			receipt := indeterminateOperationReceipt(start.Operation, time.Now().UTC(), "canonical_ledger_unavailable")
			return m.finishPulseConfirmation(id, receipt)
		}
		receipt := rejectedOperationReceipt(start.Operation, time.Now().UTC(), "delivery_unavailable")
		return m.finishPulseConfirmation(id, receipt)
	}
	if start.Recover {
		if receipt, settled := m.recoverPulseConfirmation(start.Operation); settled {
			return m.finishPulseConfirmation(id, receipt)
		}
	}

	var receipt pulseOperationReceipt
	err = devices.withActiveDevice(deviceID, func() error {
		// This write-ahead marker is inside the device lifecycle boundary and
		// immediately precedes the canonical primitive. Once revoke returns no
		// later protected execution can begin.
		if err := m.pulseOperations.markAttempt(id); err != nil {
			return err
		}
		receipt = m.executePulseOperation(start.Operation)
		return nil
	})
	if err != nil {
		if errors.Is(err, errInvalidDeviceCredential) {
			receipt = rejectedOperationReceipt(start.Operation, time.Now().UTC(), "device_inactive")
			return m.finishPulseConfirmation(id, receipt)
		}
		return pulseOperationReceipt{}, err
	}
	return m.finishPulseConfirmation(id, receipt)
}

func (m *Manager) finishPulseConfirmation(id string, receipt pulseOperationReceipt) (pulseOperationReceipt, error) {
	stored, err := m.pulseOperations.finishConfirm(id, receipt)
	if err != nil {
		// finishConfirm keeps the in-memory final receipt and refuses further
		// prepares after a persistence failure. Returning the known receipt is
		// more truthful than converting an already delivered instruction into
		// an HTTP failure.
		if stored.OperationID != "" {
			return stored, nil
		}
		return pulseOperationReceipt{}, err
	}
	return stored, nil
}

func (m *Manager) executePulseOperation(operation durableOperation) pulseOperationReceipt {
	now := time.Now().UTC()
	switch operation.Kind {
	case pulseOperationDirectedInstruction:
		if !m.instructionLedgerAvailable() {
			return rejectedOperationReceipt(operation, now, "delivery_unavailable")
		}
		return m.executePulseDirectedInstruction(operation, now)
	case pulseOperationPermissionDecision:
		return m.executePulsePermissionDecision(operation, now)
	default:
		return rejectedOperationReceipt(operation, now, "unsupported_operation")
	}
}

func (m *Manager) executePulseDirectedInstruction(operation durableOperation, now time.Time) pulseOperationReceipt {
	_, ok := m.Get(operation.Target.ID)
	if !ok || !validPulseInstructionText(operation.Text) || !m.pulseInstructionTargetCurrent(operation.Target) {
		return rejectedOperationReceipt(operation, now, "review_expired")
	}
	digest, err := m.pulseOperations.digest(
		operation.Kind,
		operation.Target.ID,
		operation.Target.Title,
		operation.Target.Project,
		operation.Text,
		operation.ExpectedAction,
	)
	if err != nil || digest != operation.PayloadDigest {
		return rejectedOperationReceipt(operation, now, "review_expired")
	}
	action, err := m.voiceInstructionExpected(operation.Target.ID, operation.Text, "pulse."+operation.ID, operation.ExpectedAction)
	if err != nil {
		var persistence *pulseInstructionPersistenceError
		if errors.As(err, &persistence) {
			m.pulseOperations.markDegraded()
			return indeterminateOperationReceipt(operation, now, "canonical_delivery_not_durable")
		}
		switch {
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrInstructionPermission), errors.Is(err, ErrInstructionScopeChanged):
			return rejectedOperationReceipt(operation, now, "review_expired")
		case errors.Is(err, ErrInstructionRateLimit):
			return rejectedOperationReceipt(operation, now, "policy_rejected")
		case errors.Is(err, errPulseInstructionLedger):
			m.pulseOperations.markDegraded()
			return rejectedOperationReceipt(operation, now, "delivery_unavailable")
		case errors.Is(err, errPulseInstructionUnknown):
			return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown")
		default:
			return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown")
		}
	}
	return pulseOperationReceipt{
		OperationID: operation.ID,
		Kind:        operation.Kind,
		Status:      "accepted",
		Action:      action,
		Delivery:    "delivered_to_agent",
		Observation: "not_observed",
		Completion:  "not_observed",
		At:          now,
	}
}

func (m *Manager) executePulsePermissionDecision(operation durableOperation, now time.Time) pulseOperationReceipt {
	session, ok := m.Get(operation.Target.ID)
	if !ok || !validPulsePermissionOperation(operation) {
		return rejectedOperationReceipt(operation, now, "review_expired")
	}
	snapshot := bus.PermissionDecisionSnapshot{
		PermissionID:       operation.PermissionID,
		ToolName:           operation.PermissionTool,
		AllowPatternDigest: operation.PermissionAllowPatternDigest,
		ArgsDigest:         operation.PermissionArgsDigest,
		RunGen:             operation.PermissionRunGen,
	}
	digest, err := m.permissionDecisionDigest(operation.Target.ID, snapshot, operation.PermissionDecision)
	if err != nil || digest != operation.PayloadDigest {
		return rejectedOperationReceipt(operation, now, "review_expired")
	}
	err = session.runtime.Bus.Execute(bus.ResolvePermissionExact{
		SessionID: session.ID,
		Snapshot:  snapshot,
		Approved:  operation.PermissionDecision == "approve_once",
	})
	if err != nil {
		return rejectedOperationReceipt(operation, now, "review_expired")
	}
	if operation.PermissionDecision == "deny" {
		return pulseOperationReceipt{
			OperationID: operation.ID,
			Kind:        operation.Kind,
			Status:      "rejected",
			Action:      "deny",
			Delivery:    "not_applicable",
			Observation: "permission_resolved",
			At:          now,
		}
	}
	return pulseOperationReceipt{
		OperationID: operation.ID,
		Kind:        operation.Kind,
		Status:      "accepted",
		Action:      "approve_once",
		Delivery:    "not_applicable",
		Observation: "permission_resolved",
		At:          now,
	}
}

func validPulsePermissionOperation(operation durableOperation) bool {
	return operation.PermissionID != "" &&
		validPulsePermissionTool(operation.PermissionTool) &&
		operation.PermissionAllowPatternDigest != "" &&
		operation.PermissionArgsDigest != "" &&
		(operation.PermissionDecision == "approve_once" || operation.PermissionDecision == "deny")
}

func validPulsePermissionTool(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 80 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

func acceptedOperationReceipt(operation durableOperation, action string, at time.Time) pulseOperationReceipt {
	return pulseOperationReceipt{
		OperationID: operation.ID,
		Kind:        operation.Kind,
		Status:      "accepted",
		Action:      action,
		Delivery:    "delivered_to_agent",
		Observation: "not_observed",
		Completion:  "not_observed",
		At:          at,
	}
}

func indeterminateOperationReceipt(operation durableOperation, now time.Time, reason string) pulseOperationReceipt {
	return pulseOperationReceipt{
		OperationID: operation.ID,
		Kind:        operation.Kind,
		Status:      "indeterminate",
		Delivery:    "indeterminate",
		Observation: "not_observed",
		Completion:  "not_observed",
		Reason:      reason,
		At:          now,
	}
}

// recoverPulseConfirmations never performs delivery. It can turn a durable
// canonical accepted record into a receipt, or settle a known uncertain
// attempt as indeterminate. If the canonical ledger itself is unavailable,
// every durable Attempted marker is terminally indeterminate: it must not be
// retried or recast as a rejection. A still-valid confirming review without an
// attempt remains available for an explicit retry once the ledger returns.
func (m *Manager) recoverPulseConfirmations() {
	if m.pulseOperations == nil {
		return
	}
	for _, operation := range m.pulseOperations.confirmingOperations() {
		receipt, settled := m.recoverPulseConfirmation(operation)
		if settled {
			if err := m.pulseOperations.finalizeRecovered(operation.ID, receipt); err != nil {
				slog.Warn("Pulse operation recovery receipt persistence failed", "operation_id", operation.ID, "error", err)
			}
		}
	}
}

func (m *Manager) recoverPulseConfirmation(operation durableOperation) (pulseOperationReceipt, bool) {
	now := time.Now().UTC()
	if operation.Kind == pulseOperationPermissionDecision {
		// Permission resolution has no durable canonical delivery ledger. The
		// write-ahead Attempted marker is therefore the final replay boundary:
		// after a crash it may already have unblocked the tool, so it is never
		// retried or recast as a rejection.
		if operation.Attempted {
			return indeterminatePermissionOperationReceipt(operation, now, "permission_outcome_unknown"), true
		}
		if !operation.ExpiresAt.After(now) {
			return rejectedOperationReceipt(operation, now, "review_expired"), true
		}
		return pulseOperationReceipt{}, false
	}
	if !m.instructionLedgerAvailable() {
		if operation.Attempted {
			return indeterminateOperationReceipt(operation, now, "canonical_ledger_unavailable"), true
		}
		return pulseOperationReceipt{}, false
	}
	outcome := m.pulseInstructionOutcome(operation.Target.ID, "pulse."+operation.ID)
	switch outcome.state {
	case "accepted":
		return acceptedOperationReceipt(operation, outcome.action, outcome.at), true
	case "attempting", "unknown":
		return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown"), true
	case "absent":
		// The operation's own durable Attempted marker is written immediately
		// before canonical delivery. An absent ledger record cannot prove that
		// delivery did not occur, so it is not safe to retry even before the
		// review expiry.
		if operation.Attempted {
			return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown"), true
		}
		if !operation.ExpiresAt.After(now) {
			return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown"), true
		}
		return pulseOperationReceipt{}, false
	default:
		return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown"), true
	}
}

func indeterminatePermissionOperationReceipt(operation durableOperation, now time.Time, reason string) pulseOperationReceipt {
	return pulseOperationReceipt{
		OperationID: operation.ID,
		Kind:        operation.Kind,
		Status:      "indeterminate",
		Delivery:    "indeterminate",
		Observation: "not_observed",
		Reason:      reason,
		At:          now,
	}
}

func (m *Manager) invalidatePulseDeviceOperations(deviceID string) {
	if m.pulseOperations != nil {
		m.pulseOperations.invalidateDevice(deviceID)
	}
}

func (m *Manager) pulseInstructionTargetCurrent(target opsInstructionTarget) bool {
	if m.ops == nil || target.ID == "" {
		return false
	}
	resolution := m.ops.Resolve(target.ID)
	if len(resolution.Candidates) != 1 {
		return false
	}
	candidate := resolution.Candidates[0]
	return candidate.Kind == ops.TargetSession && candidate.ID == target.ID && candidate.Title == target.Title && candidate.CanonicalCWD == target.Project
}

func rejectedOperationReceipt(operation durableOperation, now time.Time, reason string) pulseOperationReceipt {
	if operation.Kind == pulseOperationPermissionDecision {
		return pulseOperationReceipt{
			OperationID: operation.ID,
			Kind:        operation.Kind,
			Status:      "rejected",
			Action:      operation.PermissionDecision,
			Delivery:    "not_applicable",
			Observation: "not_observed",
			Reason:      reason,
			At:          now,
		}
	}
	return pulseOperationReceipt{
		OperationID: operation.ID,
		Kind:        operation.Kind,
		Status:      "rejected",
		Delivery:    "not_delivered",
		Observation: "not_observed",
		Completion:  "not_observed",
		Reason:      reason,
		At:          now,
	}
}

func (m *Manager) pulseOperationStatus(deviceID, id string) (pulseOperationResponse, error) {
	if m.pulseOperations == nil {
		return pulseOperationResponse{}, errPulseOperationUnavailable
	}
	operation, err := m.pulseOperations.get(id, deviceID)
	if err != nil {
		return pulseOperationResponse{}, err
	}
	if operation.Receipt != nil {
		return pulseOperationResponse{OperationID: operation.ID, Kind: operation.Kind, Status: "receipt", Receipt: operation.Receipt}, nil
	}
	if operation.State != "pending" && operation.State != "confirming" {
		return pulseOperationResponse{}, errOperationStoreUnavailable
	}
	return pulseOperationResponse{OperationID: operation.ID, Kind: operation.Kind, Status: operation.State, ExpiresAt: operation.ExpiresAt, Review: pulseOperationReviewFor(operation)}, nil
}

func pulseOperationReviewFor(operation durableOperation) *pulseOperationReview {
	if operation.Kind == pulseOperationPermissionDecision {
		return permissionDecisionReview(operation)
	}
	return directedInstructionReview(operation)
}

func newPulseOperationID() (string, error) {
	bytes := make([]byte, 18)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func validPulseOperationID(value string) bool {
	if len(value) != 24 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validPulseReference(value string, limit int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPulseInstructionText(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= instructionTextLimit
}
