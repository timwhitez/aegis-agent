package runtime

import (
	"aegis-agent/internal/session"
	"context"
	"os"
	"time"
)

// Build actual #125 journal evidence without a #126 receipt. The old recovery
// controls must exercise legacy identity rather than a newly admitted operation.
func (r *Runner) prepareLegacyApprovalForTest(ctx context.Context, req ContinueRequest) (*PreparedApproval, error) {
	var prepared *PreparedApproval
	var release func()
	err := r.store.WithApprovalLock(req.SessionID, func(scoped *session.Store) error {
		prep := r.newApprovalPreparationRunner(scoped)
		if err := prep.preflightPlanModeControl(req.SessionID, req); err != nil {
			return err
		}
		original, err := scoped.LoadState(req.SessionID)
		if err != nil {
			return err
		}
		snapshot, err := scoped.LoadApprovalSnapshot(req.SessionID)
		if err != nil {
			return err
		}
		release, err = r.acquireRunSlot(req.SessionID)
		if err != nil {
			return err
		}
		identity, _ := session.ProcessIdentity(os.Getpid())
		prep.approvalPreparation = &approvalPreparationRecord{SchemaVersion: 1, SessionID: req.SessionID, Target: *req.ApprovalTarget, Snapshot: snapshot, OriginalState: original, ClaimUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), OwnerPID: os.Getpid(), OwnerIdentity: identity}
		if err := prep.recordApprovalPreparation("validated", original); err != nil {
			return err
		}
		if _, err := prep.continueWithPreparation(ctx, req, &prepared); err != nil {
			return err
		}
		prepared.runner = r
		prepared.originalState = original
		prepared.releaseRunSlot = release
		return nil
	})
	if err != nil && release != nil {
		release()
	}
	return prepared, err
}

func readApprovalPreparationForTest(r *Runner, id string, record *approvalPreparationRecord) error {
	receipts, err := r.store.ListApprovalOperationReceipts(id)
	if err != nil {
		return err
	}
	if len(receipts) == 0 {
		return r.store.ReadArtifact(id, approvalPreparationArtifact, record)
	}
	selected := receipts[0]
	for _, receipt := range receipts {
		if receipt.UpdatedAt > selected.UpdatedAt {
			selected = receipt
		}
	}
	payload, err := decodeApprovalRecovery(selected)
	if err != nil {
		return err
	}
	*record = payload.Preparation
	return nil
}
