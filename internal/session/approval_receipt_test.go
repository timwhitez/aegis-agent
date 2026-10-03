package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aegis-agent/internal/fileutil"
)

func newReceiptTestRequest(t *testing.T, store *Store, id, requestID string) ApprovalOperationRequest {
	t.Helper()
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	return ApprovalOperationRequest{RequestID: requestID, Parameters: ApprovalParameters{Target: snapshot.Target()}}
}

func TestApprovalReceiptCorruptLedgerBlocksEveryIDAndWriter(t *testing.T) {
	cases := map[string]func(*approvalOperations){
		"schema":  func(l *approvalOperations) { l.SchemaVersion = 2 },
		"session": func(l *approvalOperations) { l.SessionID = "other_session" },
		"fingerprint": func(l *approvalOperations) {
			b := l.Requests["request_1"]
			b.Fingerprint = "bad"
			l.Requests["request_1"] = b
		},
		"operation_fingerprint": func(l *approvalOperations) {
			r := l.Operations["request_1"]
			r.Fingerprint = "bad"
			l.Operations["request_1"] = r
		},
		"canonical_identity": func(l *approvalOperations) {
			r := l.Operations["request_1"]
			r.OperationID = "request_2"
			l.Operations["request_1"] = r
		},
		"dangling_alias": func(l *approvalOperations) {
			b := l.Requests["request_1"]
			b.OperationID = "missing"
			l.Requests["request_1"] = b
		},
		"missing_index":  func(l *approvalOperations) { l.TargetAdmissions = map[string]string{} },
		"dangling_index": func(l *approvalOperations) { l.TargetAdmissions["invalid_target"] = "missing" },
		"wrong_index": func(l *approvalOperations) {
			for k, v := range l.TargetAdmissions {
				delete(l.TargetAdmissions, k)
				l.TargetAdmissions[k+"wrong"] = v
			}
		},
		"unknown_stage": func(l *approvalOperations) {
			r := l.Operations["request_1"]
			r.Stage = "unknown"
			l.Operations["request_1"] = r
		},
		"generation": func(l *approvalOperations) {
			r := l.Operations["request_1"]
			r.Recovery.RunGeneration = ""
			l.Operations["request_1"] = r
		},
		"missing_payload": func(l *approvalOperations) {
			r := l.Operations["request_1"]
			r.Recovery.Data = nil
			l.Operations["request_1"] = r
		},
		"unnormalized": func(l *approvalOperations) {
			r := l.Operations["request_1"]
			r.Parameters.Provider = " OPENAI "
			l.Operations["request_1"] = r
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			store, id := newApprovalTestStore(t)
			request := newReceiptTestRequest(t, store, id, "request_1")
			admitReceiptForTest(t, store, id, request)
			var ledger approvalOperations
			if err := json.Unmarshal(receiptLedgerBytes(t, store, id), &ledger); err != nil {
				t.Fatal(err)
			}
			change(&ledger)
			data, err := json.Marshal(ledger)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.SessionDir(id), approvalOperationsFile)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			fresh := request
			fresh.RequestID = "fresh_id"
			checks := []func() error{
				func() error { _, err := store.GetApprovalReceipt(id, request.RequestID); return err },
				func() error { _, err := store.LookupApprovalReceipt(id, fresh.RequestID, fresh.Parameters); return err },
				func() error {
					_, err := store.PrepareApprovalOperation(id, fresh, receiptRecovery("run_receipt"))
					return err
				},
				func() error { _, err := store.RejectApprovalOperation(id, fresh, "coverage denied"); return err },
				func() error { _, err := store.BindApprovalRequest(id, fresh, request.RequestID); return err },
				func() error {
					_, err := store.CheckpointApprovalOperation(id, request.RequestID, "run_receipt", "settled", receiptRecovery("run_receipt"))
					return err
				},
				func() error { _, err := store.AdmitApprovalOperation(id, request.RequestID, "run_receipt"); return err },
			}
			for i, check := range checks {
				if err := check(); !errors.Is(err, ErrApprovalReceiptUnverifiable) {
					t.Fatalf("writer/read %d bypassed corrupt ledger: %v", i, err)
				}
			}
			if !bytes.Equal(data, receiptLedgerBytes(t, store, id)) {
				t.Fatal("corrupt ledger was overwritten")
			}
		})
	}
}

func TestApprovalReceiptStrictFramingAndTypedFields(t *testing.T) {
	for _, kind := range []string{"truncated", "trailing", "invalid_utf8", "case_field", "unknown_field", "null_parameters", "missing_override", "null_maps", "unreadable_directory", "symlink", "dangling_symlink"} {
		t.Run(kind, func(t *testing.T) {
			store, id := newApprovalTestStore(t)
			request := newReceiptTestRequest(t, store, id, "request_1")
			prepareReceiptForTest(t, store, id, request)
			path := filepath.Join(store.SessionDir(id), approvalOperationsFile)
			data := receiptLedgerBytes(t, store, id)
			switch kind {
			case "truncated":
				data = data[:len(data)/2]
			case "trailing":
				data = append(data, []byte(` {}`)...)
			case "invalid_utf8":
				data = bytes.Replace(data, []byte(`"phase":"validated"`), []byte{'"', 'p', 'h', 'a', 's', 'e', '"', ':', '"', 0xff, '"'}, 1)
			case "case_field":
				data = bytes.Replace(data, []byte(`"schema_version"`), []byte(`"Schema_Version"`), 1)
			case "unknown_field":
				data = bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1)
			case "null_parameters":
				var m map[string]json.RawMessage
				json.Unmarshal(data, &m)
				var requests map[string]map[string]json.RawMessage
				json.Unmarshal(m["requests"], &requests)
				requests["request_1"]["parameters"] = json.RawMessage(`null`)
				m["requests"], _ = json.Marshal(requests)
				data, _ = json.Marshal(m)
			case "missing_override":
				data = bytes.Replace(data, []byte(`"override_coverage":false,`), nil, 1)
			case "null_maps":
				data = bytes.Replace(data, []byte(`"target_admissions":{}`), []byte(`"target_admissions":null`), 1)
			case "unreadable_directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling_symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "target")
				if kind == "symlink" {
					if err := os.WriteFile(target, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "unreadable_directory" && kind != "symlink" && kind != "dangling_symlink" {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, rid := range []string{request.RequestID, "new_request"} {
				if _, err := store.LookupApprovalReceipt(id, rid, request.Parameters); !errors.Is(err, ErrApprovalReceiptUnverifiable) {
					t.Fatalf("%s failed open under %s: %v", kind, rid, err)
				}
			}
		})
	}
}

func TestApprovalReceiptPreparedReplayAfterGoalChangeNeedsReview(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_1")
	request.Parameters.OverrideCoverage = true
	first := prepareReceiptForTest(t, store, id, request)
	if _, _, err := store.MutateGoal(id, func(goal *SessionGoal) error { goal.Objective = "Changed goal semantic scope"; return nil }); err != nil {
		t.Fatal(err)
	}
	replay, err := store.PrepareApprovalOperation(id, request, receiptRecovery("new_run"))
	if err != nil || replay.OperationID != first.OperationID || replay.Recovery.RunGeneration != first.Recovery.RunGeneration {
		t.Fatalf("prepared replay became stale or new operation: %#v %v", replay, err)
	}
	if _, err := store.CheckpointApprovalOperation(id, first.OperationID, "run_receipt", "prepared", first.Recovery); err != nil {
		t.Fatal(err)
	}
	before := receiptLedgerBytes(t, store, id)
	if _, err := store.AdmitApprovalOperation(id, first.OperationID, "run_receipt"); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("changed prepared scope admitted: %v", err)
	}
	if !bytes.Equal(before, receiptLedgerBytes(t, store, id)) {
		t.Fatal("changed scope rewrote prepared receipt")
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(id), "goal.json"), []byte(`broken goal`), 0600); err != nil {
		t.Fatal(err)
	}
	lookup, err := store.LookupApprovalReceipt(id, request.RequestID, request.Parameters)
	if err != nil || !lookup.Found {
		t.Fatalf("receipt-first read depended on unrelated current goal file: %#v %v", lookup, err)
	}
}

func TestApprovalReceiptDuplicatePreparedCanonicalFailsClosed(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_1")
	prepareReceiptForTest(t, store, id, request)
	var ledger approvalOperations
	if err := json.Unmarshal(receiptLedgerBytes(t, store, id), &ledger); err != nil {
		t.Fatal(err)
	}
	duplicate := ledger.Operations[request.RequestID]
	duplicate.OperationID = "request_2"
	ledger.Operations[duplicate.OperationID] = duplicate
	binding := ledger.Requests[request.RequestID]
	binding.RequestID = duplicate.OperationID
	binding.OperationID = duplicate.OperationID
	ledger.Requests[binding.RequestID] = binding
	data, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(id), approvalOperationsFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupApprovalReceipt(id, "new_request", request.Parameters); !errors.Is(err, ErrApprovalReceiptUnverifiable) {
		t.Fatalf("duplicate canonical target accepted: %v", err)
	}
}

func TestApprovalReceiptParametersCapturedAndCompared(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_1")
	temperature := 0.25
	flag := false
	request.Parameters.Provider = " OPENAI "
	request.Parameters.Model = " Default "
	request.Parameters.Message = " explicit message "
	request.Parameters.SystemOverride = " exact system "
	request.Parameters.ProviderOptions = ProviderOptions{Temperature: &temperature, Store: &flag, RetryPolicy: &ProviderRetryPolicy{}}
	recovery := receiptRecovery("run_receipt")
	receipt, err := store.PrepareApprovalOperation(id, request, recovery)
	if err != nil {
		t.Fatal(err)
	}
	params, err := NormalizeApprovalParameters(request.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	temperature = 0.75
	flag = true
	request.Parameters.ProviderOptions.RetryPolicy.MaxAttempts = 9
	recovery.Data[0] = 'x'
	if *receipt.Parameters.ProviderOptions.Temperature != 0.25 || *receipt.Parameters.ProviderOptions.Store || receipt.Parameters.ProviderOptions.RetryPolicy.MaxAttempts != 0 || receipt.Recovery.Data[0] != '{' {
		t.Fatal("caller mutation escaped deep parameter/payload capture")
	}
	lookup, err := store.LookupApprovalReceipt(id, request.RequestID, params)
	if err != nil || !lookup.Found {
		t.Fatalf("normalized same inputs failed: %#v %v", lookup, err)
	}
	changes := map[string]func(*ApprovalParameters){
		"target": func(p *ApprovalParameters) { p.Target.PlanVersion++ }, "coverage": func(p *ApprovalParameters) { p.OverrideCoverage = true },
		"message": func(p *ApprovalParameters) { p.Message = "other" }, "provider": func(p *ApprovalParameters) { p.Provider = "other" },
		"model": func(p *ApprovalParameters) { p.Model = "other" }, "system": func(p *ApprovalParameters) { p.SystemOverride = "other" },
		"options": func(p *ApprovalParameters) { p.ProviderOptions.MaxOutputTokens = 99 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p, err := NormalizeApprovalParameters(params)
			if err != nil {
				t.Fatal(err)
			}
			change(&p)
			if _, err := store.LookupApprovalReceipt(id, request.RequestID, p); !errors.Is(err, ErrApprovalRequestConflict) {
				t.Fatalf("changed %s accepted: %v", name, err)
			}
		})
	}
}

func TestApprovalReceiptPendingCanonicalConflictAndAdmittedAliasParameters(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_A")
	canonical := prepareReceiptForTest(t, store, id, request)
	other := request
	other.RequestID = "request_B"
	other.Parameters.OverrideCoverage = true
	if _, err := store.PrepareApprovalOperation(id, other, receiptRecovery("other_generation")); !errors.Is(err, ErrApprovalRequestConflict) {
		t.Fatalf("different pending parameters accepted: %v", err)
	}
	admitted := admitReceiptForTest(t, store, id, request)
	lookup, err := store.LookupApprovalReceipt(id, other.RequestID, other.Parameters)
	if err != nil || !lookup.Found || !lookup.NeedsAlias {
		t.Fatalf("admitted alias missed: %#v %v", lookup, err)
	}
	alias, err := store.BindApprovalRequest(id, other, canonical.OperationID)
	if err != nil || !reflect.DeepEqual(alias, admitted) {
		t.Fatalf("admitted alias changed canonical: %#v %v", alias, err)
	}
	bound, err := store.GetApprovalReceipt(id, other.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if !bound.Binding.Parameters.OverrideCoverage || bound.Receipt.Parameters.OverrideCoverage {
		t.Fatal("alias parameters replaced actual admitted parameters")
	}
}

func TestApprovalReceiptCoverageRejectBindingAndNewTargetControl(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_denied")
	denied, err := store.RejectApprovalOperation(id, request, "mission validation coverage blocks approval")
	if err != nil || denied.Stage != ApprovalReceiptRejected {
		t.Fatalf("rejection failed: %#v %v", denied, err)
	}
	if _, err := store.AdmitApprovalOperation(id, denied.OperationID, "run_receipt"); !errors.Is(err, ErrApprovalRequestConflict) {
		t.Fatalf("rejection admitted: %v", err)
	}
	confirmed := request
	confirmed.Parameters.OverrideCoverage = true
	if _, err := store.LookupApprovalReceipt(id, confirmed.RequestID, confirmed.Parameters); !errors.Is(err, ErrApprovalRequestConflict) {
		t.Fatalf("same ID coverage change accepted: %v", err)
	}
	confirmed.RequestID = "request_confirmed"
	admitted := admitReceiptForTest(t, store, id, confirmed)
	old, err := store.GetApprovalReceipt(id, request.RequestID)
	if err != nil || old.Receipt.Stage != ApprovalReceiptRejected {
		t.Fatalf("original rejection lost: %#v %v", old, err)
	}
	if err := store.WithApprovalLock(id, func(scoped *Store) error {
		ledger, err := scoped.readApprovalOperations(id)
		if err != nil {
			return err
		}
		if len(ledger.TargetAdmissions) != 1 || len(ledger.Operations) != 2 {
			return errors.New("rejected operation reserved admission index")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MutatePlanMode(id, func(plan *PlanModeState) error { plan.Objective = "New exact target"; return nil }); err != nil {
		t.Fatal(err)
	}
	next := newReceiptTestRequest(t, store, id, "request_next")
	next.Parameters.OverrideCoverage = true
	newReceipt := prepareReceiptForTest(t, store, id, next)
	if newReceipt.OperationID == admitted.OperationID {
		t.Fatal("old admission blocked distinct new target")
	}
	stale := request
	stale.RequestID = "request_stale"
	// A previously admitted target correctly aliases even after scope changes.
	// Exercise CAS with a target that has no historical operation instead.
	stale.Parameters.Target.PlanVersion += 99
	before := receiptLedgerBytes(t, store, id)
	if _, err := store.PrepareApprovalOperation(id, stale, receiptRecovery("run_stale")); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("stale new operation accepted: %v", err)
	}
	if !bytes.Equal(before, receiptLedgerBytes(t, store, id)) {
		t.Fatal("stale CAS wrote ledger")
	}
}

func TestApprovalReceiptBlankGenerationBindsOnceAndAdmissionNeverRegresses(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_1")
	receipt, err := store.PrepareApprovalOperation(id, request, receiptRecovery(""))
	if err != nil {
		t.Fatal(err)
	}
	bound := receiptRecovery("run_bound")
	receipt, err = store.CheckpointApprovalOperation(id, receipt.OperationID, "", "prepared", bound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckpointApprovalOperation(id, receipt.OperationID, "run_bound", "prepared", receiptRecovery("new_run")); !errors.Is(err, ErrApprovalRequestConflict) {
		t.Fatalf("bound generation changed: %v", err)
	}
	receipt, err = store.AdmitApprovalOperation(id, receipt.OperationID, "run_bound")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"aborted", "review_required", "prepare_failed", "settled"} {
		receipt, err = store.CheckpointApprovalOperation(id, receipt.OperationID, "run_bound", phase, bound)
		if err != nil || receipt.Stage != ApprovalReceiptAdmitted || receipt.AdmittedAt == "" {
			t.Fatalf("admission regressed: %#v %v", receipt, err)
		}
	}
	if _, err := store.AdmitApprovalOperation(id, receipt.OperationID, "new_run"); !errors.Is(err, ErrApprovalRequestConflict) {
		t.Fatalf("wrong generation admitted replay accepted: %v", err)
	}
}

func TestApprovalReceiptCommitFailuresPreserveAdmissionKnowledge(t *testing.T) {
	cases := []struct {
		stage    fileutil.AtomicCommitStage
		outcome  fileutil.AtomicCommitOutcome
		admitted bool
	}{
		{fileutil.AtomicCommitBeforeWrite, fileutil.AtomicCommitNotPublished, false}, {fileutil.AtomicCommitBeforeFileSync, fileutil.AtomicCommitNotPublished, false},
		{fileutil.AtomicCommitBeforePublish, fileutil.AtomicCommitNotPublished, false}, {fileutil.AtomicCommitAfterPublish, fileutil.AtomicCommitPublishedUnconfirmed, true},
		{fileutil.AtomicCommitBeforeDirectorySync, fileutil.AtomicCommitPublishedUnconfirmed, true}, {fileutil.AtomicCommitAfterCommit, fileutil.AtomicCommitCommitted, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.stage), func(t *testing.T) {
			store, id := newApprovalTestStore(t)
			request := newReceiptTestRequest(t, store, id, "request_1")
			receipt := prepareReceiptForTest(t, store, id, request)
			if _, err := store.CheckpointApprovalOperation(id, receipt.OperationID, "run_receipt", "prepared", receipt.Recovery); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("commit boundary failure")
			store.beforeApprovalReceiptCommit = func(stage fileutil.AtomicCommitStage) error {
				if stage == tc.stage {
					return cause
				}
				return nil
			}
			_, err := store.AdmitApprovalOperation(id, receipt.OperationID, "run_receipt")
			var commitErr *ApprovalReceiptCommitError
			if !errors.As(err, &commitErr) || commitErr.Outcome != tc.outcome || !errors.Is(err, cause) {
				t.Fatalf("lost publication outcome: %v", err)
			}
			restarted := NewStore(store.Root())
			actual, err := restarted.GetApprovalReceipt(id, request.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			want := ApprovalReceiptPrepared
			if tc.admitted {
				want = ApprovalReceiptAdmitted
			}
			if actual.Receipt.Stage != want {
				t.Fatalf("unexpected recovered marker: %s", actual.Receipt.Stage)
			}
			alias := request
			alias.RequestID = "request_retry"
			replay, err := restarted.PrepareApprovalOperation(id, alias, receiptRecovery("new_run"))
			if err != nil {
				t.Fatal(err)
			}
			if replay.OperationID != receipt.OperationID || replay.Stage != want || replay.Recovery.RunGeneration != "run_receipt" {
				t.Fatalf("retry escaped old operation: %#v", replay)
			}
		})
	}
}

func TestApprovalReceiptCapacityRetainsIdentitiesAndExactReadBound(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_1")
	prepareReceiptForTest(t, store, id, request)
	if err := store.WithApprovalLock(id, func(scoped *Store) error {
		ledger, err := scoped.readApprovalOperations(id)
		if err != nil {
			return err
		}
		receipt := ledger.Operations[request.RequestID]
		receipt.Recovery.Data = json.RawMessage(`{"schema_version":1,"padding":""}`)
		ledger.Operations[request.RequestID] = receipt
		encoded, err := json.Marshal(ledger)
		if err != nil {
			return err
		}
		padding := int(ApprovalReceiptMaxBytes) - len(encoded)
		receipt.Recovery.Data = json.RawMessage(`{"schema_version":1,"padding":"` + strings.Repeat("x", padding) + `"}`)
		ledger.Operations[request.RequestID] = receipt
		encoded, err = json.Marshal(ledger)
		if err != nil {
			return err
		}
		if int64(len(encoded)) != ApprovalReceiptMaxBytes {
			return errors.New("fixture did not reach exact read bound")
		}
		return scoped.writeApprovalOperations(ledger)
	}); err != nil {
		t.Fatal(err)
	}
	before := receiptLedgerBytes(t, store, id)
	if _, err := NewStore(store.Root()).GetApprovalReceipt(id, request.RequestID); err != nil {
		t.Fatalf("exact maximum write unreadable: %v", err)
	}
	alias := request
	alias.RequestID = "request_over_capacity"
	if _, err := store.BindApprovalRequest(id, alias, request.RequestID); !errors.Is(err, ErrApprovalReceiptCapacity) {
		t.Fatalf("capacity overflow accepted: %v", err)
	}
	if !bytes.Equal(before, receiptLedgerBytes(t, store, id)) {
		t.Fatal("capacity failure pruned retained identities")
	}
	if _, err := NewStore(store.Root()).GetApprovalReceipt(id, request.RequestID); err != nil {
		t.Fatalf("capacity failure damaged old receipt: %v", err)
	}
	path := filepath.Join(store.SessionDir(id), approvalOperationsFile)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(" "); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := store.LookupApprovalReceipt(id, alias.RequestID, alias.Parameters); !errors.Is(err, ErrApprovalReceiptUnverifiable) {
		t.Fatalf("oversize ledger bypassed read bound: %v", err)
	}
}

func TestApprovalReceiptConcurrentStoresOneCanonicalAdmission(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "unused")
	var wg sync.WaitGroup
	var admissions atomic.Int32
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			other := NewStore(store.Root())
			req := request
			req.RequestID = fmt.Sprintf("request_%d", i)
			errs <- other.WithApprovalLock(id, func(scoped *Store) error {
				receipt, err := scoped.PrepareApprovalOperation(id, req, receiptRecovery("run_receipt"))
				if err != nil {
					return err
				}
				if receipt.Stage == ApprovalReceiptAdmitted {
					return nil
				}
				if _, err := scoped.CheckpointApprovalOperation(id, receipt.OperationID, "run_receipt", "prepared", receipt.Recovery); err != nil {
					return err
				}
				if _, err := scoped.AdmitApprovalOperation(id, receipt.OperationID, "run_receipt"); err != nil {
					return err
				}
				admissions.Add(1)
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if admissions.Load() != 1 {
		t.Fatalf("admissions=%d", admissions.Load())
	}
	if err := store.WithApprovalLock(id, func(scoped *Store) error {
		ledger, err := scoped.readApprovalOperations(id)
		if err != nil {
			return err
		}
		if len(ledger.Requests) != 12 || len(ledger.Operations) != 1 || len(ledger.TargetAdmissions) != 1 {
			return errors.New("concurrent stores created multiple operations or lost aliases")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalReceiptCrossProcessLockAndAlias(t *testing.T) {
	if root := os.Getenv("AEGIS_RECEIPT_TEST_ROOT"); root != "" {
		id := os.Getenv("AEGIS_RECEIPT_TEST_SESSION")
		marker := os.Getenv("AEGIS_RECEIPT_TEST_MARKER")
		if err := os.WriteFile(marker+".started", nil, 0600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(root)
		old, err := store.GetApprovalReceipt(id, "request_parent")
		if err != nil {
			t.Fatal(err)
		}
		request := ApprovalOperationRequest{RequestID: "request_child", Parameters: old.Binding.Parameters}
		receipt, err := store.PrepareApprovalOperation(id, request, receiptRecovery("other_generation"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CheckpointApprovalOperation(id, receipt.OperationID, "run_receipt", "prepared", receipt.Recovery); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AdmitApprovalOperation(id, receipt.OperationID, "run_receipt"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker+".done", nil, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_parent")
	prepareReceiptForTest(t, store, id, request)
	marker := filepath.Join(t.TempDir(), "child")
	child := exec.Command(os.Args[0], "-test.run=^TestApprovalReceiptCrossProcessLockAndAlias$", "-test.timeout=20s")
	child.Env = append(os.Environ(), "AEGIS_RECEIPT_TEST_ROOT="+store.Root(), "AEGIS_RECEIPT_TEST_SESSION="+id, "AEGIS_RECEIPT_TEST_MARKER="+marker)
	var output bytes.Buffer
	child.Stdout = &output
	child.Stderr = &output
	err := store.WithApprovalLock(id, func(_ *Store) error {
		if err := child.Start(); err != nil {
			return err
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(marker + ".started"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("child did not start")
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
		if _, err := os.Stat(marker + ".done"); !errors.Is(err, os.ErrNotExist) {
			return errors.New("child bypassed approval lock")
		}
		return nil
	})
	if err != nil {
		if child.Process != nil {
			child.Process.Kill()
			child.Wait()
		}
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("child failed: %v %s", err, output.String())
	}
	lookup, err := store.GetApprovalReceipt(id, "request_child")
	if err != nil || lookup.Receipt.OperationID != "request_parent" || lookup.Receipt.Stage != ApprovalReceiptAdmitted {
		t.Fatalf("cross-process alias diverged: %#v %v", lookup, err)
	}
}

func TestApprovalReceiptMissingIDUnknownSessionAndExpiredScope(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_1")
	if _, err := store.LookupApprovalReceipt(id, "", request.Parameters); !errors.Is(err, ErrMissingApprovalRequestID) {
		t.Fatalf("missing ID upgrade sentinel lost: %v", err)
	}
	if _, err := store.LookupApprovalReceipt("unknown_session", request.RequestID, request.Parameters); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown session read: %v", err)
	}
	if _, err := os.Stat(store.SessionDir("unknown_session")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("query created unknown session")
	}
	var retained *Store
	if err := store.WithApprovalLock(id, func(scoped *Store) error {
		retained = scoped
		_, err := scoped.PrepareApprovalOperation(id, request, receiptRecovery("run_receipt"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := retained.GetApprovalReceipt(id, request.RequestID); !errors.Is(err, ErrApprovalScopeClosed) {
		t.Fatalf("expired scope reused: %v", err)
	}
}

func receiptRecovery(generation string) ApprovalRecoveryPayload {
	return ApprovalRecoveryPayload{SchemaVersion: 1, RunGeneration: generation, OwnerPID: os.Getpid(), Data: json.RawMessage(`{"schema_version":1,"phase":"validated"}`)}
}

func prepareReceiptForTest(t *testing.T, store *Store, id string, request ApprovalOperationRequest) ApprovalReceipt {
	t.Helper()
	receipt, err := store.PrepareApprovalOperation(id, request, receiptRecovery("run_receipt"))
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func admitReceiptForTest(t *testing.T, store *Store, id string, request ApprovalOperationRequest) ApprovalReceipt {
	t.Helper()
	receipt := prepareReceiptForTest(t, store, id, request)
	receipt, err := store.CheckpointApprovalOperation(id, receipt.OperationID, receipt.Recovery.RunGeneration, "prepared", receipt.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = store.AdmitApprovalOperation(id, receipt.OperationID, receipt.Recovery.RunGeneration)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func receiptLedgerBytes(t *testing.T, store *Store, id string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store.SessionDir(id), approvalOperationsFile))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestApprovalReceiptStrictDuplicateKeys(t *testing.T) {
	for _, kind := range []string{"top_level", "request_identity", "operation_identity", "parameters", "recovery", "escaped_key"} {
		t.Run(kind, func(t *testing.T) {
			store, id := newApprovalTestStore(t)
			request := newReceiptTestRequest(t, store, id, "request_1")
			prepareReceiptForTest(t, store, id, request)
			data := string(receiptLedgerBytes(t, store, id))
			switch kind {
			case "top_level":
				data = strings.Replace(data, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)
			case "request_identity", "operation_identity":
				var ledger map[string]json.RawMessage
				if err := json.Unmarshal([]byte(data), &ledger); err != nil {
					t.Fatal(err)
				}
				field := "requests"
				if kind == "operation_identity" {
					field = "operations"
				}
				var entries map[string]json.RawMessage
				if err := json.Unmarshal(ledger[field], &entries); err != nil {
					t.Fatal(err)
				}
				ledger[field] = json.RawMessage(fmt.Sprintf(`{"request_1":%s,"request_1":%s}`, entries["request_1"], entries["request_1"]))
				encoded, err := json.Marshal(ledger)
				if err != nil {
					t.Fatal(err)
				}
				data = string(encoded)
			case "parameters":
				data = strings.Replace(data, `"override_coverage":false`, `"override_coverage":false,"override_coverage":false`, 1)
			case "recovery":
				data = strings.Replace(data, `"data":{"schema_version":1`, `"data":{"schema_version":1,"schema_version":1`, 1)
			case "escaped_key":
				data = strings.Replace(data, `"schema_version":1`, `"schema_version":1,"\u0073chema_version":1`, 1)
			}
			if err := os.WriteFile(filepath.Join(store.SessionDir(id), approvalOperationsFile), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			for _, requestID := range []string{request.RequestID, "new_request"} {
				if _, err := store.LookupApprovalReceipt(id, requestID, request.Parameters); !errors.Is(err, ErrApprovalReceiptUnverifiable) {
					t.Fatalf("duplicate %s accepted under %s: %v", kind, requestID, err)
				}
			}
		})
	}
}

func TestApprovalReceiptMissingLedgerFieldsFailClosed(t *testing.T) {
	for _, field := range []string{"schema_version", "session_id", "requests", "operations", "target_admissions"} {
		t.Run(field, func(t *testing.T) {
			store, id := newApprovalTestStore(t)
			request := newReceiptTestRequest(t, store, id, "request_1")
			prepareReceiptForTest(t, store, id, request)
			var ledger map[string]json.RawMessage
			if err := json.Unmarshal(receiptLedgerBytes(t, store, id), &ledger); err != nil {
				t.Fatal(err)
			}
			delete(ledger, field)
			data, err := json.Marshal(ledger)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.SessionDir(id), approvalOperationsFile), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LookupApprovalReceipt(id, "new_request", request.Parameters); !errors.Is(err, ErrApprovalReceiptUnverifiable) {
				t.Fatalf("missing %s accepted: %v", field, err)
			}
		})
	}
}

func TestApprovalReceiptLegalPreparedAliasAndReceiptFirst(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_A")
	first := prepareReceiptForTest(t, store, id, request)
	alias := request
	alias.RequestID = "request_B"
	second := prepareReceiptForTest(t, NewStore(store.Root()), id, alias)
	if second.OperationID != first.OperationID {
		t.Fatal("prepared target created a second canonical operation")
	}
	admitted := admitReceiptForTest(t, store, id, alias)
	if admitted.OperationID != first.OperationID {
		t.Fatal("alias admitted a different operation")
	}
	if _, _, err := store.MutatePlanMode(id, func(plan *PlanModeState) error { plan.Objective = "New reviewed scope"; return nil }); err != nil {
		t.Fatal(err)
	}
	lookup, err := NewStore(store.Root()).LookupApprovalReceipt(id, request.RequestID, request.Parameters)
	if err != nil || !lookup.Found || lookup.Receipt.Stage != ApprovalReceiptAdmitted || lookup.NeedsAlias {
		t.Fatalf("existing receipt lost after scope change: %#v %v", lookup, err)
	}
	if !reflect.DeepEqual(admitted, lookup.Receipt) {
		t.Fatal("replay changed canonical receipt")
	}
	changed := request
	changed.Parameters.OverrideCoverage = true
	if _, err := store.LookupApprovalReceipt(id, changed.RequestID, changed.Parameters); !errors.Is(err, ErrApprovalRequestConflict) {
		t.Fatalf("changed parameters accepted: %v", err)
	}
}

func TestApprovalReceiptGenerationLookupCanonicalAndIndependentOfGoal(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_canonical")
	request.Parameters.OverrideCoverage = true
	canonical := prepareReceiptForTest(t, store, id, request)
	alias := request
	alias.RequestID = "request_alias"
	if _, err := store.BindApprovalRequest(id, alias, canonical.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MutateGoal(id, func(goal *SessionGoal) error {
		goal.Objective = "A later mission objective"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Corrupt current goal content also cannot affect a ledger-only owner lookup.
	if err := os.WriteFile(filepath.Join(store.SessionDir(id), "goal.json"), []byte(`broken current goal`), 0600); err != nil {
		t.Fatal(err)
	}
	before := receiptLedgerBytes(t, store, id)
	lookup, err := NewStore(store.Root()).LookupApprovalOperationByGeneration(id, canonical.Recovery.RunGeneration)
	if err != nil || !lookup.Found || lookup.NeedsAlias || lookup.Binding.RequestID != canonical.OperationID || lookup.Binding.OperationID != canonical.OperationID || !reflect.DeepEqual(lookup.Receipt, canonical) {
		t.Fatalf("generation did not return canonical receipt/binding: %#v %v", lookup, err)
	}
	for _, generation := range []string{"", "unknown_generation"} {
		lookup, err := store.LookupApprovalOperationByGeneration(id, generation)
		if !errors.Is(err, os.ErrNotExist) || lookup.Found {
			t.Fatalf("unknown generation returned operation: %#v %v", lookup, err)
		}
	}
	if !bytes.Equal(before, receiptLedgerBytes(t, store, id)) {
		t.Fatal("read-only generation lookup wrote ledger")
	}
}

func TestApprovalReceiptGenerationLookupCorruptLedgerFailsClosed(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_1")
	prepareReceiptForTest(t, store, id, request)
	path := filepath.Join(store.SessionDir(id), approvalOperationsFile)
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"requests":`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []string{"run_receipt", "unknown_generation", ""} {
		lookup, err := store.LookupApprovalOperationByGeneration(id, generation)
		if !errors.Is(err, ErrApprovalReceiptUnverifiable) || lookup.Found {
			t.Fatalf("generation query bypassed corrupt ledger: %#v %v", lookup, err)
		}
	}
}

func TestApprovalReceiptGenerationLookupAmbiguousCanonicalFailsClosed(t *testing.T) {
	store, id := newApprovalTestStore(t)
	request := newReceiptTestRequest(t, store, id, "request_1")
	prepareReceiptForTest(t, store, id, request)
	if _, _, err := store.MutatePlanMode(id, func(plan *PlanModeState) error {
		plan.Objective = "A distinct reviewed target"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := newReceiptTestRequest(t, store, id, "request_2")
	prepareReceiptForTest(t, store, id, second)
	// The existing ledger validator permits distinct targets with equal run
	// generation. This owner lookup must reject that ambiguity independently.
	if _, err := store.GetApprovalReceipt(id, request.RequestID); err != nil {
		t.Fatalf("fixture did not pass existing full-ledger validation: %v", err)
	}
	lookup, err := store.LookupApprovalOperationByGeneration(id, "run_receipt")
	if !errors.Is(err, ErrApprovalReceiptUnverifiable) || lookup.Found {
		t.Fatalf("ambiguous generation chose an arbitrary operation: %#v %v", lookup, err)
	}
}

func TestApprovalReceiptListCanonicalSortedAndGoalIndependent(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	first := newReceiptTestRequest(t, store, id, "operation_z")
	first.Parameters.OverrideCoverage = true
	prepareReceiptForTest(t, store, id, first)
	alias := first
	alias.RequestID = "alias_z"
	if _, err := store.BindApprovalRequest(id, alias, first.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MutateGoal(id, func(goal *SessionGoal) error {
		goal.Objective = "A distinct mission target"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := newReceiptTestRequest(t, store, id, "operation_a")
	second.Parameters.OverrideCoverage = true
	prepareReceiptForTest(t, store, id, second)
	alias = second
	alias.RequestID = "alias_a"
	if _, err := store.BindApprovalRequest(id, alias, second.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(id), "goal.json"), []byte(`broken current goal`), 0600); err != nil {
		t.Fatal(err)
	}
	before := receiptLedgerBytes(t, store, id)
	var retained *Store
	if err := store.WithApprovalLock(id, func(scoped *Store) error {
		retained = scoped
		receipts, err := scoped.ListApprovalOperationReceipts(id)
		if err != nil {
			return err
		}
		if len(receipts) != 2 || receipts[0].OperationID != second.RequestID || receipts[1].OperationID != first.RequestID {
			return fmt.Errorf("list returned aliases or unstable canonical order: %#v", receipts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	receipts, err := NewStore(store.Root()).ListApprovalOperationReceipts(id)
	if err != nil || len(receipts) != 2 || receipts[0].OperationID != second.RequestID || receipts[1].OperationID != first.RequestID {
		t.Fatalf("reloaded list differed: %#v %v", receipts, err)
	}
	originalPayload := append([]byte(nil), receipts[0].Recovery.Data...)
	receipts[0].Recovery.Data[0] = 'x'
	receipts[0].OperationID = "caller_mutated"
	again, err := store.ListApprovalOperationReceipts(id)
	if err != nil || len(again) != 2 || again[0].OperationID != second.RequestID || !bytes.Equal(again[0].Recovery.Data, originalPayload) {
		t.Fatalf("returned values changed durable receipts: %#v %v", again, err)
	}
	if !bytes.Equal(before, receiptLedgerBytes(t, store, id)) {
		t.Fatal("read-only canonical list rewrote ledger")
	}
	if _, err := retained.ListApprovalOperationReceipts(id); !errors.Is(err, ErrApprovalScopeClosed) {
		t.Fatalf("expired scoped list bypassed coordination: %v", err)
	}
}

func TestApprovalReceiptListEmptyAndCorruptClosed(t *testing.T) {
	store, id := newApprovalTestStore(t)
	receipts, err := store.ListApprovalOperationReceipts(id)
	if err != nil || receipts == nil || len(receipts) != 0 {
		t.Fatalf("empty ledger did not return an empty array: %#v %v", receipts, err)
	}
	path := filepath.Join(store.SessionDir(id), approvalOperationsFile)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty list created ledger")
	}
	if _, err := store.ListApprovalOperationReceipts("unknown_session"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown session list returned success: %v", err)
	}
	request := newReceiptTestRequest(t, store, id, "operation_1")
	prepareReceiptForTest(t, store, id, request)
	data := receiptLedgerBytes(t, store, id)
	for _, corrupt := range [][]byte{
		[]byte(`{"schema_version":1,"operations":`),
		bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		bytes.Replace(data, []byte(`"target_admissions":{}`), []byte(`"target_admissions":{"bad":"missing"}`), 1),
	} {
		if err := os.WriteFile(path, corrupt, 0600); err != nil {
			t.Fatal(err)
		}
		receipts, err := store.ListApprovalOperationReceipts(id)
		if !errors.Is(err, ErrApprovalReceiptUnverifiable) || len(receipts) != 0 {
			t.Fatalf("canonical list bypassed corrupt ledger: %#v %v", receipts, err)
		}
	}
}
