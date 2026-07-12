package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/ops"
)

const (
	pulseOperationTTL     = 5 * time.Minute
	pulseReceiptRetention = 24 * time.Hour
	maxPulseOperations    = 1024
)

const (
	pulseOperationInstruction = "directed_instruction"
	pulseOperationPermission  = "permission_decision"
)

var (
	errPulseOperationsUnavailable = errors.New("pulse operations unavailable")
	errPulseOperationConflict     = errors.New("request_id was already used with different operation")
	errPulseOperationNotFound     = errors.New("operation not found")
)

type operationStore struct {
	mu   sync.Mutex
	path string
}

type durableOperationState struct {
	Key        string                  `json:"key"`
	Operations []durablePulseOperation `json:"operations"`
}

type durablePulseOperation struct {
	ID              string                 `json:"id"`
	Kind            string                 `json:"kind"`
	RequestID       string                 `json:"request_id"`
	Fingerprint     string                 `json:"fingerprint"`
	PreparedBy      string                 `json:"prepared_by"`
	PreparedAt      time.Time              `json:"prepared_at"`
	ExpiresAt       time.Time              `json:"expires_at"`
	Target          pulseOperationTarget   `json:"target"`
	CurrentState    string                 `json:"current_state"`
	Scope           string                 `json:"scope"`
	Risk            string                 `json:"risk"`
	Review          string                 `json:"review"`
	InstructionText string                 `json:"instruction_text,omitempty"`
	PermissionID    string                 `json:"permission_id,omitempty"`
	Decision        string                 `json:"decision,omitempty"`
	ToolName        string                 `json:"tool_name,omitempty"`
	Status          string                 `json:"status"`
	Receipt         *pulseOperationReceipt `json:"receipt,omitempty"`
}

type pulseOperation struct {
	durablePulseOperation
}

type pulseOperationTarget struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project,omitempty"`
}

type pulseOperationReceipt struct {
	OperationID     string               `json:"operation_id"`
	Target          pulseOperationTarget `json:"target"`
	Status          string               `json:"status"`
	DeliveryStatus  string               `json:"delivery_status"`
	ObservedStatus  string               `json:"observed_status"`
	Timestamp       time.Time            `json:"timestamp"`
	RequestIdentity string               `json:"request_identity"`
}

type pulseOperationView struct {
	OperationID  string                 `json:"operation_id"`
	Kind         string                 `json:"kind"`
	Target       pulseOperationTarget   `json:"target"`
	CurrentState string                 `json:"current_state"`
	Scope        string                 `json:"scope"`
	Risk         string                 `json:"risk"`
	Review       string                 `json:"review"`
	PreparedAt   time.Time              `json:"prepared_at"`
	ExpiresAt    time.Time              `json:"expires_at"`
	Status       string                 `json:"status"`
	Receipt      *pulseOperationReceipt `json:"receipt,omitempty"`
}

func openOperationStore(path string) (*operationStore, durableOperationState, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, durableOperationState{}, fmt.Errorf("create operation directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, durableOperationState{}, fmt.Errorf("secure operation directory: %w", err)
	}
	store := &operationStore{path: path}
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, durableOperationState{}, nil
	}
	if err != nil {
		return nil, durableOperationState{}, fmt.Errorf("read operation store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, durableOperationState{}, fmt.Errorf("secure operation store: %w", err)
	}
	var state durableOperationState
	if err := json.Unmarshal(contents, &state); err != nil {
		return nil, durableOperationState{}, fmt.Errorf("decode operation store: %w", err)
	}
	return store, state, nil
}

func (s *operationStore) save(state durableOperationState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	contents, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".pulse-operations-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(contents); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

func (m *Manager) restorePulseOperations(records []durablePulseOperation) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	now := m.operationNow().UTC()
	for _, record := range records {
		if !validPulseOperation(record) {
			continue
		}
		op := &pulseOperation{durablePulseOperation: record}
		if op.Status == "confirming" {
			op.Status = "rejected"
			op.InstructionText = ""
			op.Review = ""
			op.Receipt = m.newPulseReceipt(op, "rejected", "not delivered", "outcome unavailable after service restart", now)
		}
		m.operations[op.ID] = op
	}
	m.prunePulseOperationsLocked(now)
}

func validPulseOperation(record durablePulseOperation) bool {
	if !validInstructionID(record.ID) || !validInstructionID(record.RequestID) || record.PreparedBy == "" || record.PreparedAt.IsZero() || record.ExpiresAt.IsZero() || record.Target.ID == "" || record.Fingerprint == "" {
		return false
	}
	if record.Kind != pulseOperationInstruction && record.Kind != pulseOperationPermission {
		return false
	}
	if record.Status != "pending" && record.Status != "confirming" && record.Status != "accepted" && record.Status != "rejected" && record.Status != "expired" {
		return false
	}
	if record.Status == "pending" && (record.CurrentState == "" || record.Scope == "" || record.Risk == "" || record.Review == "") {
		return false
	}
	switch record.Kind {
	case pulseOperationInstruction:
		return record.Status != "pending" || record.InstructionText != ""
	case pulseOperationPermission:
		return record.PermissionID != "" && (record.Decision == "approve" || record.Decision == "deny")
	}
	return false
}

func (m *Manager) persistPulseOperationsLocked() error {
	if m.operationStore == nil {
		return errPulseOperationsUnavailable
	}
	state := durableOperationState{Key: encodeInstructionKey(m.operationKey)}
	for _, op := range m.operations {
		state.Operations = append(state.Operations, op.durablePulseOperation)
	}
	return m.operationStore.save(state)
}

func (m *Manager) prunePulseOperationsLocked(now time.Time) {
	for id, op := range m.operations {
		if op.Status == "pending" && !op.ExpiresAt.After(now) {
			op.Status = "expired"
			op.InstructionText = ""
			op.Review = ""
			op.Receipt = m.newPulseReceipt(op, "expired", "not delivered", "confirmation window expired", now)
		}
		if op.Status == "confirming" && !op.ExpiresAt.After(now) {
			op.Status = "rejected"
			op.InstructionText = ""
			op.Review = ""
			op.Receipt = m.newPulseReceipt(op, "rejected", "not delivered", "outcome unavailable after interrupted confirmation", now)
		}
		if (op.Status == "accepted" || op.Status == "rejected" || op.Status == "expired") && op.PreparedAt.Before(now.Add(-pulseReceiptRetention)) {
			delete(m.operations, id)
		}
	}
	if len(m.operations) <= maxPulseOperations {
		return
	}
	type ref struct {
		id string
		at time.Time
	}
	refs := make([]ref, 0, len(m.operations))
	for id, op := range m.operations {
		refs = append(refs, ref{id: id, at: op.PreparedAt})
	}
	for len(refs) > maxPulseOperations {
		oldest := 0
		for i := range refs {
			if refs[i].at.Before(refs[oldest].at) {
				oldest = i
			}
		}
		delete(m.operations, refs[oldest].id)
		refs = append(refs[:oldest], refs[oldest+1:]...)
	}
}

func (m *Manager) pulseFingerprint(parts ...string) string {
	mac := hmac.New(sha256.New, m.operationKey)
	for _, part := range parts {
		_, _ = mac.Write([]byte(part))
		_, _ = mac.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *Manager) preparePulseOperation(op *pulseOperation) (pulseOperationView, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	if m.operationStore == nil {
		return pulseOperationView{}, errPulseOperationsUnavailable
	}
	now := m.operationNow().UTC()
	m.prunePulseOperationsLocked(now)
	for _, current := range m.operations {
		if current.Kind != op.Kind || current.PreparedBy != op.PreparedBy || current.RequestID != op.RequestID {
			continue
		}
		if !hmac.Equal([]byte(current.Fingerprint), []byte(op.Fingerprint)) {
			return pulseOperationView{}, errPulseOperationConflict
		}
		if err := m.persistPulseOperationsLocked(); err != nil {
			return pulseOperationView{}, err
		}
		return pulseOperationViewFor(current), nil
	}
	op.PreparedAt = now
	op.ExpiresAt = now.Add(pulseOperationTTL)
	op.Status = "pending"
	m.operations[op.ID] = op
	if err := m.persistPulseOperationsLocked(); err != nil {
		delete(m.operations, op.ID)
		return pulseOperationView{}, err
	}
	return pulseOperationViewFor(op), nil
}

func pulseOperationViewFor(op *pulseOperation) pulseOperationView {
	return pulseOperationView{
		OperationID:  op.ID,
		Kind:         op.Kind,
		Target:       op.Target,
		CurrentState: op.CurrentState,
		Scope:        op.Scope,
		Risk:         op.Risk,
		Review:       op.Review,
		PreparedAt:   op.PreparedAt,
		ExpiresAt:    op.ExpiresAt,
		Status:       op.Status,
		Receipt:      op.Receipt,
	}
}

func (m *Manager) newPulseReceipt(op *pulseOperation, status, delivery, observed string, at time.Time) *pulseOperationReceipt {
	return &pulseOperationReceipt{
		OperationID:     op.ID,
		Target:          op.Target,
		Status:          status,
		DeliveryStatus:  delivery,
		ObservedStatus:  observed,
		Timestamp:       at,
		RequestIdentity: op.PreparedBy,
	}
}

func (m *Manager) rejectPulseOperationLocked(op *pulseOperation, observed string, now time.Time) (pulseOperationView, error) {
	op.Status = "rejected"
	op.InstructionText = ""
	op.Review = ""
	op.Receipt = m.newPulseReceipt(op, "rejected", "not delivered", observed, now)
	if err := m.persistPulseOperationsLocked(); err != nil {
		return pulseOperationView{}, err
	}
	return pulseOperationViewFor(op), nil
}

func (m *Manager) confirmPulseOperation(id string) (pulseOperationView, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	if m.operationStore == nil {
		return pulseOperationView{}, errPulseOperationsUnavailable
	}
	now := m.operationNow().UTC()
	m.prunePulseOperationsLocked(now)
	op, ok := m.operations[id]
	if !ok {
		return pulseOperationView{}, errPulseOperationNotFound
	}
	if op.Status != "pending" {
		if err := m.persistPulseOperationsLocked(); err != nil {
			return pulseOperationView{}, err
		}
		return pulseOperationViewFor(op), nil
	}
	if !op.ExpiresAt.After(now) {
		op.Status = "expired"
		op.InstructionText = ""
		op.Review = ""
		op.Receipt = m.newPulseReceipt(op, "expired", "not delivered", "confirmation window expired", now)
		if err := m.persistPulseOperationsLocked(); err != nil {
			return pulseOperationView{}, err
		}
		return pulseOperationViewFor(op), nil
	}
	op.Status = "confirming"
	if err := m.persistPulseOperationsLocked(); err != nil {
		op.Status = "pending"
		return pulseOperationView{}, err
	}

	switch op.Kind {
	case pulseOperationInstruction:
		sess, ok := m.Get(op.Target.ID)
		if !ok {
			return m.rejectPulseOperationLocked(op, "target no longer exists", now)
		}
		current := string(sess.runtime.State.Current())
		if current != op.CurrentState || (current != string(bus.StateIdle) && current != string(bus.StateError) && current != string(bus.StateRunning)) {
			return m.rejectPulseOperationLocked(op, "session state changed; prepare a new operation", now)
		}
		action, err := m.VoiceInstruction(op.Target.ID, op.InstructionText, op.ID)
		if err != nil {
			return m.rejectPulseOperationLocked(op, "instruction was not accepted by the current session policy", now)
		}
		op.Status = "accepted"
		op.InstructionText = ""
		op.Review = ""
		op.Receipt = m.newPulseReceipt(op, "accepted", "delivered as "+action, string(sess.runtime.State.Current()), m.operationNow().UTC())
	case pulseOperationPermission:
		sess, ok := m.Get(op.Target.ID)
		if !ok || string(sess.runtime.State.Current()) != op.CurrentState {
			return m.rejectPulseOperationLocked(op, "permission state changed; prepare a new operation", now)
		}
		pending, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{})
		if err != nil || pending.Permission == nil || pending.Permission.ID != op.PermissionID {
			return m.rejectPulseOperationLocked(op, "permission request is no longer pending", now)
		}
		if err := sess.runtime.Bus.Execute(bus.ResolvePermission{PermissionID: op.PermissionID, Approved: op.Decision == "approve"}); err != nil {
			return m.rejectPulseOperationLocked(op, "permission request could not be resolved", now)
		}
		op.Status = "accepted"
		op.Review = ""
		op.Receipt = m.newPulseReceipt(op, "accepted", "one-off permission decision delivered", string(sess.runtime.State.Current()), m.operationNow().UTC())
	default:
		return m.rejectPulseOperationLocked(op, "unknown operation type", now)
	}
	if err := m.persistPulseOperationsLocked(); err != nil {
		return pulseOperationView{}, err
	}
	return pulseOperationViewFor(op), nil
}

func (m *Manager) pulseOperation(id string) (pulseOperationView, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	if m.operationStore == nil {
		return pulseOperationView{}, errPulseOperationsUnavailable
	}
	m.prunePulseOperationsLocked(m.operationNow().UTC())
	op, ok := m.operations[id]
	if !ok {
		return pulseOperationView{}, errPulseOperationNotFound
	}
	if err := m.persistPulseOperationsLocked(); err != nil {
		return pulseOperationView{}, err
	}
	return pulseOperationViewFor(op), nil
}

func (m *Manager) resolvePulseInstructionTarget(target string) (ops.Candidate, *ManagedSession, []opsInstructionTarget, error) {
	if m.ops == nil {
		return ops.Candidate{}, nil, nil, errPulseOperationsUnavailable
	}
	resolution := m.ops.Resolve(target)
	if len(resolution.Candidates) == 0 {
		return ops.Candidate{}, nil, nil, ErrNotFound
	}
	if len(resolution.Candidates) != 1 {
		candidates := make([]opsInstructionTarget, len(resolution.Candidates))
		for i, candidate := range resolution.Candidates {
			candidates[i] = safeOpsInstructionTarget(candidate)
		}
		return ops.Candidate{}, nil, candidates, nil
	}
	candidate := resolution.Candidates[0]
	if candidate.Kind != ops.TargetSession || candidate.ID == "" {
		return ops.Candidate{}, nil, nil, ErrNotFound
	}
	sess, ok := m.Get(candidate.ID)
	if !ok {
		return ops.Candidate{}, nil, nil, ErrNotFound
	}
	return candidate, sess, nil, nil
}

func safePulseToolName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 80 {
		return "requested"
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "requested"
		}
	}
	return value
}
