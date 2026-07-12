package serve

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
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

var (
	errPulseOperationUnavailable = errors.New("Pulse operations unavailable")
	errPulseOperationReviewStale = errors.New("operation review is no longer current")
)

type pulseOperationPrepareBody struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Text   string `json:"text"`
}

type pulseOperationReview struct {
	Target      opsInstructionTarget `json:"target"`
	Text        string               `json:"text"`
	Action      string               `json:"action"`
	Risk        string               `json:"risk"`
	Consequence string               `json:"consequence"`
}

type pulseOperationReceipt struct {
	OperationID string    `json:"operation_id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Action      string    `json:"action,omitempty"`
	Delivery    string    `json:"delivery"`
	Observation string    `json:"observation"`
	Completion  string    `json:"completion"`
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
		var body pulseOperationPrepareBody
		if !decodeInstructionBody(w, r, &body) {
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
		case errors.Is(err, ErrInstructionPermission), errors.Is(err, errPulseOperationReviewStale):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "operation cannot be reviewed in the target's current state"})
		case errors.Is(err, errOperationAdmission):
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Pulse operation capacity reached; retry after existing reviews or receipts expire"})
		case errors.Is(err, errPulseOperationUnavailable), errors.Is(err, errOperationStoreUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Pulse operations temporarily unavailable"})
		case err != nil:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusCreated, response)
		}
	}
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
	if body.Kind != pulseOperationDirectedInstruction {
		return pulseOperationResponse{}, nil, errors.New("unsupported Pulse operation kind")
	}
	return m.preparePulseDirectedInstruction(deviceID, body.Target, body.Text)
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
		return m.executePulseDirectedInstruction(operation, now)
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
// attempt as indeterminate. A still-valid confirming review without a ledger
// attempt remains available for an explicit retry; that retry first writes
// both operation and canonical write-ahead records before it can execute.
func (m *Manager) recoverPulseConfirmations() {
	if m.pulseOperations == nil {
		return
	}
	for _, operation := range m.pulseOperations.confirmingOperations() {
		receipt, settled := m.recoverPulseConfirmation(operation)
		if settled {
			_ = m.pulseOperations.finalizeRecovered(operation.ID, receipt)
		}
	}
}

func (m *Manager) recoverPulseConfirmation(operation durableOperation) (pulseOperationReceipt, bool) {
	now := time.Now().UTC()
	outcome := m.pulseInstructionOutcome(operation.Target.ID, "pulse."+operation.ID)
	switch outcome.state {
	case "accepted":
		return acceptedOperationReceipt(operation, outcome.action, outcome.at), true
	case "attempting", "unknown":
		return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown"), true
	case "absent":
		if !operation.ExpiresAt.After(now) {
			return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown"), true
		}
		return pulseOperationReceipt{}, false
	default:
		return indeterminateOperationReceipt(operation, now, "delivery_outcome_unknown"), true
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
	return pulseOperationResponse{OperationID: operation.ID, Kind: operation.Kind, Status: operation.State, ExpiresAt: operation.ExpiresAt, Review: directedInstructionReview(operation)}, nil
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
