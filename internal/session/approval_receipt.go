package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"aegis-agent/internal/fileutil"
)

const ApprovalReceiptSchemaVersion = 1
const ApprovalReceiptMaxBytes = fileutil.MaxRegularFileReadBytes
const approvalOperationsFile = "approval-operations.json"

type ApprovalReceiptStage string

const (
	ApprovalReceiptRejected ApprovalReceiptStage = "rejected"
	ApprovalReceiptPrepared ApprovalReceiptStage = "prepared"
	ApprovalReceiptAdmitted ApprovalReceiptStage = "admitted"
)

var (
	ErrMissingApprovalRequestID    = errors.New("approval_request_id is required; upgrade the client and supply an explicit approval operation identity")
	ErrApprovalRequestConflict     = fmt.Errorf("%w: approval request parameters or pending operation conflict", ErrApprovalConflict)
	ErrApprovalReceiptUnverifiable = errors.New("approval control facts cannot be verified; explicit recovery is required")
	ErrApprovalReceiptCapacity     = errors.New("approval receipt ledger exceeds its retained identity capacity")
)

// ApprovalParameters captures explicit inputs, not resolved Config defaults.
// Normalization deep-copies ProviderOptions so caller-owned pointers cannot
// change a request after it has been bound. Source and callbacks are excluded.
type ApprovalParameters struct {
	SchemaVersion    int             `json:"schema_version"`
	Target           ApprovalTarget  `json:"target"`
	OverrideCoverage bool            `json:"override_coverage"`
	Message          string          `json:"message,omitempty"`
	Provider         string          `json:"provider,omitempty"`
	Model            string          `json:"model,omitempty"`
	ProviderOptions  ProviderOptions `json:"provider_options"`
	SystemOverride   string          `json:"system,omitempty"`
}

type ApprovalOperationRequest struct {
	RequestID  string             `json:"approval_request_id"`
	Parameters ApprovalParameters `json:"parameters"`
}

// Data is runtime-owned, versioned recovery content. The envelope identifies
// the run claim without introducing another generation mechanism. The session
// ledger validates its JSON framing; runtime validates its recovery semantics.
type ApprovalRecoveryPayload struct {
	SchemaVersion int             `json:"schema_version"`
	RunGeneration string          `json:"run_generation,omitempty"`
	OwnerPID      int             `json:"owner_pid"`
	OwnerIdentity string          `json:"owner_identity,omitempty"`
	Data          json.RawMessage `json:"data"`
}

type ApprovalRequestBinding struct {
	RequestID   string             `json:"approval_request_id"`
	OperationID string             `json:"operation_id"`
	Parameters  ApprovalParameters `json:"parameters"`
	Fingerprint string             `json:"fingerprint"`
	CreatedAt   string             `json:"created_at"`
}

type ApprovalReceipt struct {
	SchemaVersion int                     `json:"schema_version"`
	SessionID     string                  `json:"session_id"`
	OperationID   string                  `json:"operation_id"`
	Target        ApprovalTarget          `json:"target"`
	Parameters    ApprovalParameters      `json:"parameters"`
	Fingerprint   string                  `json:"fingerprint"`
	Stage         ApprovalReceiptStage    `json:"stage"`
	Phase         string                  `json:"phase"`
	Recovery      ApprovalRecoveryPayload `json:"recovery"`
	Rejection     string                  `json:"rejection,omitempty"`
	CreatedAt     string                  `json:"created_at"`
	UpdatedAt     string                  `json:"updated_at"`
	AdmittedAt    string                  `json:"admitted_at,omitempty"`
}

type ApprovalReceiptLookup struct {
	Found   bool                   `json:"found"`
	Binding ApprovalRequestBinding `json:"binding"`
	Receipt ApprovalReceipt        `json:"receipt"`
	// A target match has not bound this new request ID until Bind succeeds.
	NeedsAlias bool `json:"needs_alias,omitempty"`
}

type approvalOperations struct {
	SchemaVersion    int                               `json:"schema_version"`
	SessionID        string                            `json:"session_id"`
	Requests         map[string]ApprovalRequestBinding `json:"requests"`
	Operations       map[string]ApprovalReceipt        `json:"operations"`
	TargetAdmissions map[string]string                 `json:"target_admissions"`
}

// ApprovalReceiptCommitError preserves publication knowledge even if the
// writer failed after its durable commit. It must not trigger admission rollback.
type ApprovalReceiptCommitError struct {
	Outcome fileutil.AtomicCommitOutcome
	Err     error
}

func (e *ApprovalReceiptCommitError) Error() string {
	return fmt.Sprintf("commit approval receipt (%s): %v", e.Outcome, e.Err)
}
func (e *ApprovalReceiptCommitError) Unwrap() error { return e.Err }

func NormalizeApprovalParameters(input ApprovalParameters) (ApprovalParameters, error) {
	if input.SchemaVersion != 0 && input.SchemaVersion != ApprovalReceiptSchemaVersion {
		return ApprovalParameters{}, errors.New("unsupported approval parameter schema")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return ApprovalParameters{}, fmt.Errorf("capture approval parameters: %w", err)
	}
	var params ApprovalParameters
	if err := json.Unmarshal(data, &params); err != nil {
		return params, err
	}
	params.SchemaVersion = ApprovalReceiptSchemaVersion
	params.Provider = strings.ToLower(strings.TrimSpace(params.Provider))
	if params.Provider == "default" {
		params.Provider = ""
	}
	params.Model = strings.TrimSpace(params.Model)
	if strings.EqualFold(params.Model, "default") {
		params.Model = ""
	}
	params.Message = strings.TrimSpace(params.Message)
	// System text remains exact; its whitespace may be significant to callers.
	if err := validateApprovalTargetIdentity(params.Target); err != nil {
		return ApprovalParameters{}, err
	}
	return params, nil
}

func ApprovalParameterFingerprint(input ApprovalParameters) (string, error) {
	params, err := NormalizeApprovalParameters(input)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "approval-parameters-v1:" + hex.EncodeToString(sum[:]), nil
}

func validateApprovalTargetIdentity(target ApprovalTarget) error {
	if target.PlanModeID == "" || target.PlanVersion <= 0 || target.ExpectedRevision == "" {
		return ErrMissingApprovalTarget
	}
	if err := validateStoreID("plan mode", target.PlanModeID); err != nil {
		return err
	}
	if strings.TrimSpace(target.ExpectedRevision) != target.ExpectedRevision {
		return errors.New("approval revision has surrounding whitespace")
	}
	return nil
}

func validateApprovalRequestID(id string) error {
	if id == "" {
		return ErrMissingApprovalRequestID
	}
	if len(id) > 256 || !utf8.ValidString(id) {
		return errors.New("invalid approval request id")
	}
	if err := validateStoreID("approval request", id); err != nil {
		return err
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("invalid approval request id")
		}
	}
	return nil
}

func approvalTargetKey(sessionID string, target ApprovalTarget) string {
	data, _ := json.Marshal(struct {
		SessionID string         `json:"session_id"`
		Target    ApprovalTarget `json:"target"`
	}{sessionID, target})
	sum := sha256.Sum256(data)
	return "approval-target-v1:" + hex.EncodeToString(sum[:])
}

func normalizeApprovalRequest(request ApprovalOperationRequest) (ApprovalOperationRequest, string, error) {
	if err := validateApprovalRequestID(request.RequestID); err != nil {
		return request, "", err
	}
	params, err := NormalizeApprovalParameters(request.Parameters)
	if err != nil {
		return request, "", err
	}
	request.Parameters = params
	fingerprint, err := ApprovalParameterFingerprint(params)
	return request, fingerprint, err
}

func (s *Store) withApprovalOperations(sessionID string, fn func(*Store, *approvalOperations) error) error {
	return s.WithApprovalLock(sessionID, func(scoped *Store) error {
		// No public Store call is made while holding mu. The outer coordination
		// boundary covers the entire read/modify/write, including snapshot CAS.
		ledger, err := scoped.readApprovalOperations(sessionID)
		if err != nil {
			return err
		}
		return fn(scoped, &ledger)
	})
}

func (s *Store) readApprovalOperations(sessionID string) (approvalOperations, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger := approvalOperations{SchemaVersion: 1, SessionID: sessionID, Requests: map[string]ApprovalRequestBinding{}, Operations: map[string]ApprovalReceipt{}, TargetAdmissions: map[string]string{}}
	path, err := s.sessionPath(sessionID, approvalOperationsFile)
	if err != nil {
		return ledger, err
	}
	data, _, err := fileutil.ReadRegularFileNoSymlink(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return ledger, fmt.Errorf("%w: read %s: %v", ErrApprovalReceiptUnverifiable, approvalOperationsFile, err)
	}
	if err := decodeApprovalLedger(data, &ledger); err != nil {
		return ledger, fmt.Errorf("%w: decode %s: %v", ErrApprovalReceiptUnverifiable, approvalOperationsFile, err)
	}
	if err := validateApprovalOperations(sessionID, ledger); err != nil {
		return ledger, fmt.Errorf("%w: %v", ErrApprovalReceiptUnverifiable, err)
	}
	return ledger, nil
}

func decodeApprovalLedger(data []byte, target *approvalOperations) error {
	if err := validateUniqueApprovalJSON(data); err != nil {
		return err
	}
	if err := validateApprovalJSONFields(data, reflect.TypeFor[approvalOperations]()); err != nil {
		return err
	}
	var decoded approvalOperations
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing approval ledger content")
	}
	*target = decoded
	return nil
}

// encoding/json silently accepts repeated keys (including escaped equivalents)
// and case-insensitive struct fields. Durable identities require stricter input.
func validateUniqueApprovalJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("approval JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return errors.New("approval JSON nesting exceeds limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate or invalid approval JSON key %q", key)
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("unterminated approval JSON object")
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("unterminated approval JSON array")
			}
		default:
			return errors.New("invalid approval JSON delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing approval JSON content")
	}
	return nil
}

func validateApprovalJSONFields(data []byte, typ reflect.Type) error {
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return nil
		}
		return validateApprovalJSONFields(data, typ.Elem())
	}
	if typ.Kind() != reflect.Struct && typ.Kind() != reflect.Map {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return errors.New("approval JSON object is required")
	}
	if typ.Kind() == reflect.Map {
		for _, raw := range object {
			if err := validateApprovalJSONFields(raw, typ.Elem()); err != nil {
				return err
			}
		}
		return nil
	}
	fields := map[string]reflect.StructField{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		parts := strings.Split(tag, ",")
		if parts[0] == "-" || field.PkgPath != "" {
			continue
		}
		name := parts[0]
		if name == "" {
			name = field.Name
		}
		fields[name] = field
		optional := false
		for _, option := range parts[1:] {
			optional = optional || option == "omitempty"
		}
		if _, exists := object[name]; !optional && !exists {
			return fmt.Errorf("missing approval JSON field %s", name)
		}
	}
	for name, raw := range object {
		field, ok := fields[name]
		if !ok {
			return fmt.Errorf("unknown approval JSON field %s", name)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && field.Type.Kind() != reflect.Pointer && field.Type != reflect.TypeFor[json.RawMessage]() {
			return fmt.Errorf("null approval JSON field %s", name)
		}
		if err := validateApprovalJSONFields(raw, field.Type); err != nil {
			return err
		}
	}
	return nil
}

func validateApprovalOperations(sessionID string, ledger approvalOperations) error {
	if ledger.SchemaVersion != 1 || ledger.SessionID != sessionID || ledger.Requests == nil || ledger.Operations == nil || ledger.TargetAdmissions == nil {
		return errors.New("invalid approval ledger schema or session")
	}
	pending := map[string]string{}
	for id, receipt := range ledger.Operations {
		if err := validateApprovalRequestID(id); err != nil {
			return err
		}
		if receipt.SchemaVersion != 1 || receipt.SessionID != sessionID || receipt.OperationID != id || receipt.Target != receipt.Parameters.Target {
			return errors.New("contradictory approval operation identity")
		}
		params, err := NormalizeApprovalParameters(receipt.Parameters)
		if err != nil || !reflect.DeepEqual(params, receipt.Parameters) {
			return errors.New("invalid normalized operation parameters")
		}
		fingerprint, err := ApprovalParameterFingerprint(params)
		if err != nil || fingerprint != receipt.Fingerprint {
			return errors.New("contradictory operation fingerprint")
		}
		created, err := approvalReceiptTime(receipt.CreatedAt)
		if err != nil {
			return err
		}
		updated, err := approvalReceiptTime(receipt.UpdatedAt)
		if err != nil || updated.Before(created) {
			return errors.New("invalid approval operation timestamp")
		}
		if strings.TrimSpace(receipt.Phase) != receipt.Phase || receipt.Phase == "" || len(receipt.Phase) > 96 {
			return errors.New("invalid approval preparation phase")
		}
		key := approvalTargetKey(sessionID, receipt.Target)
		switch receipt.Stage {
		case ApprovalReceiptRejected:
			if receipt.Rejection == "" || receipt.Phase != "rejected" || receipt.AdmittedAt != "" || !emptyApprovalRecovery(receipt.Recovery) {
				return errors.New("invalid rejected approval operation")
			}
		case ApprovalReceiptPrepared, ApprovalReceiptAdmitted:
			if receipt.Rejection != "" {
				return errors.New("accepted operation has rejection")
			}
			if err := validateApprovalRecovery(receipt.Recovery); err != nil {
				return err
			}
			if receipt.Stage == ApprovalReceiptAdmitted {
				admitted, err := approvalReceiptTime(receipt.AdmittedAt)
				if err != nil || admitted.Before(created) || updated.Before(admitted) || receipt.Recovery.RunGeneration == "" || ledger.TargetAdmissions[key] != id {
					return errors.New("invalid admitted approval operation or target index")
				}
			} else {
				if receipt.AdmittedAt != "" || ledger.TargetAdmissions[key] != "" || pending[key] != "" {
					return errors.New("duplicate or contradictory prepared approval target")
				}
				pending[key] = id
			}
		default:
			return errors.New("unknown approval receipt stage")
		}
		binding, ok := ledger.Requests[id]
		if !ok || binding.OperationID != id || binding.Fingerprint != receipt.Fingerprint {
			return errors.New("missing canonical approval binding")
		}
	}
	for id, binding := range ledger.Requests {
		if err := validateApprovalRequestID(id); err != nil {
			return err
		}
		receipt, ok := ledger.Operations[binding.OperationID]
		if !ok || binding.RequestID != id {
			return errors.New("dangling or contradictory approval alias")
		}
		params, err := NormalizeApprovalParameters(binding.Parameters)
		if err != nil || !reflect.DeepEqual(params, binding.Parameters) {
			return errors.New("invalid normalized binding parameters")
		}
		fingerprint, err := ApprovalParameterFingerprint(params)
		if err != nil || fingerprint != binding.Fingerprint {
			return errors.New("contradictory binding fingerprint")
		}
		if _, err := approvalReceiptTime(binding.CreatedAt); err != nil {
			return err
		}
		if id == receipt.OperationID {
			if binding.Fingerprint != receipt.Fingerprint {
				return errors.New("canonical binding parameters differ")
			}
		} else if binding.Parameters.Target != receipt.Target || receipt.Stage == ApprovalReceiptRejected || (receipt.Stage == ApprovalReceiptPrepared && binding.Fingerprint != receipt.Fingerprint) {
			return errors.New("invalid prepared or admitted alias")
		}
	}
	for key, id := range ledger.TargetAdmissions {
		receipt, ok := ledger.Operations[id]
		if !ok || receipt.Stage != ApprovalReceiptAdmitted || approvalTargetKey(sessionID, receipt.Target) != key {
			return errors.New("dangling or contradictory admission index")
		}
	}
	return nil
}

func approvalReceiptTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || t.IsZero() || t.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("invalid approval receipt time")
	}
	return t, nil
}

func validateApprovalRecovery(recovery ApprovalRecoveryPayload) error {
	if recovery.SchemaVersion != 1 || recovery.OwnerPID <= 0 || len(recovery.Data) == 0 {
		return errors.New("invalid approval recovery envelope")
	}
	if err := validateUniqueApprovalJSON(recovery.Data); err != nil {
		return err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(recovery.Data, &value); err != nil || value == nil {
		return errors.New("invalid approval recovery payload")
	}
	if strings.TrimSpace(recovery.RunGeneration) != recovery.RunGeneration || len(recovery.RunGeneration) > 256 {
		return errors.New("invalid approval recovery generation")
	}
	return nil
}

func emptyApprovalRecovery(recovery ApprovalRecoveryPayload) bool {
	return recovery.SchemaVersion == 0 && recovery.RunGeneration == "" && recovery.OwnerPID == 0 && recovery.OwnerIdentity == "" && (len(recovery.Data) == 0 || bytes.Equal(bytes.TrimSpace(recovery.Data), []byte("null")))
}

func captureApprovalRecovery(recovery ApprovalRecoveryPayload) ApprovalRecoveryPayload {
	recovery.Data = append(json.RawMessage(nil), recovery.Data...)
	return recovery
}

func (s *Store) writeApprovalOperations(ledger approvalOperations) error {
	if err := validateApprovalOperations(ledger.SessionID, ledger); err != nil {
		return fmt.Errorf("%w: %v", ErrApprovalReceiptUnverifiable, err)
	}
	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	if int64(len(data)) > ApprovalReceiptMaxBytes {
		return ErrApprovalReceiptCapacity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(ledger.SessionID, approvalOperationsFile)
	if err != nil {
		return err
	}
	outcome, err := fileutil.AtomicCommitFileNoSymlink(path, data, s.fileMode, fileutil.AtomicCommitOptions{BeforeStage: s.beforeApprovalReceiptCommit})
	if err != nil {
		return &ApprovalReceiptCommitError{Outcome: outcome, Err: err}
	}
	return nil
}

func lookupApprovalOperation(ledger *approvalOperations, request ApprovalOperationRequest, fingerprint string) (ApprovalReceiptLookup, error) {
	if binding, ok := ledger.Requests[request.RequestID]; ok {
		if binding.Fingerprint != fingerprint {
			return ApprovalReceiptLookup{}, ErrApprovalRequestConflict
		}
		return ApprovalReceiptLookup{Found: true, Binding: binding, Receipt: ledger.Operations[binding.OperationID]}, nil
	}
	key := approvalTargetKey(ledger.SessionID, request.Parameters.Target)
	operationID := ledger.TargetAdmissions[key]
	if operationID == "" {
		for id, receipt := range ledger.Operations {
			if receipt.Stage == ApprovalReceiptPrepared && receipt.Target == request.Parameters.Target {
				if receipt.Fingerprint != fingerprint {
					return ApprovalReceiptLookup{}, ErrApprovalRequestConflict
				}
				operationID = id
				break
			}
		}
	}
	if operationID == "" {
		return ApprovalReceiptLookup{}, nil
	}
	return ApprovalReceiptLookup{Found: true, NeedsAlias: true, Binding: ApprovalRequestBinding{RequestID: request.RequestID, OperationID: operationID, Parameters: request.Parameters, Fingerprint: fingerprint}, Receipt: ledger.Operations[operationID]}, nil
}

func (s *Store) LookupApprovalReceipt(sessionID, requestID string, params ApprovalParameters) (result ApprovalReceiptLookup, err error) {
	request, fingerprint, err := normalizeApprovalRequest(ApprovalOperationRequest{RequestID: requestID, Parameters: params})
	if err != nil {
		return result, err
	}
	err = s.withApprovalOperations(sessionID, func(_ *Store, ledger *approvalOperations) error {
		var err error
		result, err = lookupApprovalOperation(ledger, request, fingerprint)
		return err
	})
	return result, err
}

func (s *Store) GetApprovalReceipt(sessionID, requestID string) (result ApprovalReceiptLookup, err error) {
	if err := validateApprovalRequestID(requestID); err != nil {
		return result, err
	}
	err = s.withApprovalOperations(sessionID, func(_ *Store, ledger *approvalOperations) error {
		binding, ok := ledger.Requests[requestID]
		if !ok {
			return fmt.Errorf("approval receipt %s: %w", requestID, fs.ErrNotExist)
		}
		result = ApprovalReceiptLookup{Found: true, Binding: binding, Receipt: ledger.Operations[binding.OperationID]}
		return nil
	})
	return result, err
}

// LookupApprovalOperationByGeneration reads the canonical operation behind a
// run claim. It validates the full ledger first and never reads current goal,
// plan, or provider defaults. Multiple matches are ambiguous ownership evidence,
// not permission to choose an arbitrary operation from map iteration order.
func (s *Store) LookupApprovalOperationByGeneration(sessionID, runGeneration string) (result ApprovalReceiptLookup, err error) {
	err = s.withApprovalOperations(sessionID, func(_ *Store, ledger *approvalOperations) error {
		if runGeneration == "" {
			return fmt.Errorf("approval operation generation: %w", fs.ErrNotExist)
		}
		var matched *ApprovalReceipt
		for _, receipt := range ledger.Operations {
			if receipt.Recovery.RunGeneration != runGeneration {
				continue
			}
			if matched != nil {
				return fmt.Errorf("%w: multiple canonical approval operations have run generation %s", ErrApprovalReceiptUnverifiable, runGeneration)
			}
			copy := receipt
			matched = &copy
		}
		if matched == nil {
			return fmt.Errorf("approval operation generation %s: %w", runGeneration, fs.ErrNotExist)
		}
		result = ApprovalReceiptLookup{Found: true, Binding: ledger.Requests[matched.OperationID], Receipt: *matched}
		return nil
	})
	return result, err
}

func bindApprovalRequest(ledger *approvalOperations, request ApprovalOperationRequest, fingerprint, operationID string) (ApprovalReceipt, error) {
	receipt, ok := ledger.Operations[operationID]
	if !ok {
		return ApprovalReceipt{}, fmt.Errorf("approval operation: %w", fs.ErrNotExist)
	}
	if binding, ok := ledger.Requests[request.RequestID]; ok {
		if binding.OperationID != operationID || binding.Fingerprint != fingerprint {
			return ApprovalReceipt{}, ErrApprovalRequestConflict
		}
		return receipt, nil
	}
	if receipt.Target != request.Parameters.Target || receipt.Stage == ApprovalReceiptRejected || (receipt.Stage == ApprovalReceiptPrepared && receipt.Fingerprint != fingerprint) {
		return ApprovalReceipt{}, ErrApprovalRequestConflict
	}
	ledger.Requests[request.RequestID] = ApprovalRequestBinding{RequestID: request.RequestID, OperationID: operationID, Parameters: request.Parameters, Fingerprint: fingerprint, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	return receipt, nil
}

func (s *Store) BindApprovalRequest(sessionID string, request ApprovalOperationRequest, canonicalID string) (receipt ApprovalReceipt, err error) {
	request, fingerprint, err := normalizeApprovalRequest(request)
	if err != nil {
		return receipt, err
	}
	err = s.withApprovalOperations(sessionID, func(scoped *Store, ledger *approvalOperations) error {
		var err error
		receipt, err = bindApprovalRequest(ledger, request, fingerprint, canonicalID)
		if err != nil {
			return err
		}
		return scoped.writeApprovalOperations(*ledger)
	})
	return receipt, err
}

func (s *Store) PrepareApprovalOperation(sessionID string, request ApprovalOperationRequest, recovery ApprovalRecoveryPayload) (receipt ApprovalReceipt, err error) {
	return s.createApprovalOperation(sessionID, request, recovery, "")
}

func (s *Store) RejectApprovalOperation(sessionID string, request ApprovalOperationRequest, reason string) (receipt ApprovalReceipt, err error) {
	if strings.TrimSpace(reason) == "" {
		return receipt, errors.New("approval rejection reason is required")
	}
	return s.createApprovalOperation(sessionID, request, ApprovalRecoveryPayload{}, reason)
}

func (s *Store) createApprovalOperation(sessionID string, request ApprovalOperationRequest, recovery ApprovalRecoveryPayload, rejection string) (receipt ApprovalReceipt, err error) {
	request, fingerprint, err := normalizeApprovalRequest(request)
	if err != nil {
		return receipt, err
	}
	err = s.withApprovalOperations(sessionID, func(scoped *Store, ledger *approvalOperations) error {
		lookup, err := lookupApprovalOperation(ledger, request, fingerprint)
		if err != nil {
			return err
		}
		if lookup.Found {
			receipt = lookup.Receipt
			if !lookup.NeedsAlias {
				return nil
			}
			receipt, err = bindApprovalRequest(ledger, request, fingerprint, receipt.OperationID)
			if err != nil {
				return err
			}
			return scoped.writeApprovalOperations(*ledger)
		}
		// Existing receipts never read current goal/plan. Only new operations
		// perform the reviewed-scope comparison before any ledger mutation.
		snapshot, err := scoped.LoadApprovalSnapshot(sessionID)
		if err != nil {
			return err
		}
		if err := ValidateApprovalTarget(snapshot, request.Parameters.Target); err != nil {
			return err
		}
		if rejection == "" && snapshot.Coverage != nil && snapshot.Coverage.ApprovalBlocked && !request.Parameters.OverrideCoverage {
			return errors.New("mission validation coverage blocks approval")
		}
		if rejection == "" {
			if err := validateApprovalRecovery(recovery); err != nil {
				return err
			}
		}
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		receipt = ApprovalReceipt{SchemaVersion: 1, SessionID: sessionID, OperationID: request.RequestID, Target: request.Parameters.Target, Parameters: request.Parameters, Fingerprint: fingerprint, Stage: ApprovalReceiptPrepared, Phase: "validated", Recovery: captureApprovalRecovery(recovery), CreatedAt: stamp, UpdatedAt: stamp}
		if rejection != "" {
			receipt.Stage = ApprovalReceiptRejected
			receipt.Phase = "rejected"
			receipt.Rejection = rejection
		}
		ledger.Operations[receipt.OperationID] = receipt
		ledger.Requests[request.RequestID] = ApprovalRequestBinding{RequestID: request.RequestID, OperationID: receipt.OperationID, Parameters: request.Parameters, Fingerprint: fingerprint, CreatedAt: stamp}
		return scoped.writeApprovalOperations(*ledger)
	})
	return receipt, err
}

func (s *Store) CheckpointApprovalOperation(sessionID, operationID, generation, phase string, recovery ApprovalRecoveryPayload) (receipt ApprovalReceipt, err error) {
	if err := validateApprovalRequestID(operationID); err != nil {
		return receipt, err
	}
	err = s.withApprovalOperations(sessionID, func(scoped *Store, ledger *approvalOperations) error {
		var ok bool
		receipt, ok = ledger.Operations[operationID]
		if !ok {
			return fmt.Errorf("approval operation: %w", fs.ErrNotExist)
		}
		if receipt.Stage == ApprovalReceiptRejected || receipt.Recovery.RunGeneration != generation || (generation != "" && recovery.RunGeneration != generation) {
			return ErrApprovalRequestConflict
		}
		if err := validateApprovalRecovery(recovery); err != nil {
			return err
		}
		// Once bound, run generation is immutable. Admitted cannot regress even
		// when its provider never started or its adapter could not add a handle.
		receipt.Phase = phase
		receipt.Recovery = captureApprovalRecovery(recovery)
		receipt.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		ledger.Operations[operationID] = receipt
		return scoped.writeApprovalOperations(*ledger)
	})
	return receipt, err
}

func (s *Store) AdmitApprovalOperation(sessionID, operationID, generation string) (receipt ApprovalReceipt, err error) {
	if err := validateApprovalRequestID(operationID); err != nil {
		return receipt, err
	}
	err = s.withApprovalOperations(sessionID, func(scoped *Store, ledger *approvalOperations) error {
		var ok bool
		receipt, ok = ledger.Operations[operationID]
		if !ok {
			return fmt.Errorf("approval operation: %w", fs.ErrNotExist)
		}
		if generation == "" || receipt.Recovery.RunGeneration != generation {
			return ErrApprovalRequestConflict
		}
		if receipt.Stage == ApprovalReceiptAdmitted {
			return nil
		}
		if receipt.Stage != ApprovalReceiptPrepared || receipt.Phase != "prepared" || generation == "" || receipt.Recovery.RunGeneration != generation {
			return ErrApprovalRequestConflict
		}
		key := approvalTargetKey(sessionID, receipt.Target)
		if ledger.TargetAdmissions[key] != "" {
			return ErrApprovalRequestConflict
		}
		snapshot, err := scoped.LoadApprovalSnapshot(sessionID)
		if err != nil {
			return err
		}
		if err := ValidateApprovalTarget(snapshot, receipt.Target); err != nil {
			return err
		}
		receipt.Stage = ApprovalReceiptAdmitted
		receipt.Phase = "admitted"
		receipt.AdmittedAt = time.Now().UTC().Format(time.RFC3339Nano)
		receipt.UpdatedAt = receipt.AdmittedAt
		ledger.Operations[operationID] = receipt
		ledger.TargetAdmissions[key] = operationID
		return scoped.writeApprovalOperations(*ledger)
	})
	return receipt, err
}
