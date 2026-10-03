package agent

import (
	"context"

	"aegis-agent/internal/config"
	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

type StartRequest = runtime.StartRequest
type ContinueRequest = runtime.ContinueRequest
type ApprovalTarget = session.ApprovalTarget
type ApprovalSnapshot = session.ApprovalSnapshot
type PreparedApproval = runtime.PreparedApproval
type ApprovalPreparationResult = runtime.ApprovalPreparationResult
type ApprovalResult = runtime.ApprovalResult
type ApprovalPreparationOutcomeError = runtime.ApprovalPreparationOutcomeError
type ApprovalReceiptLookup = session.ApprovalReceiptLookup
type ApprovalReceipt = session.ApprovalReceipt
type ApprovalParameters = session.ApprovalParameters
type ApprovalReceiptStage = session.ApprovalReceiptStage
type ApprovalReceiptCommitError = session.ApprovalReceiptCommitError
type AtomicCommitOutcome = fileutil.AtomicCommitOutcome

var ErrApprovalConflict = session.ErrApprovalConflict
var ErrMissingApprovalTarget = session.ErrMissingApprovalTarget
var ErrMissingApprovalRequestID = session.ErrMissingApprovalRequestID
var ErrApprovalRequestConflict = session.ErrApprovalRequestConflict
var ErrApprovalReceiptUnverifiable = session.ErrApprovalReceiptUnverifiable

const (
	ApprovalReceiptRejected          = session.ApprovalReceiptRejected
	ApprovalReceiptPrepared          = session.ApprovalReceiptPrepared
	ApprovalReceiptAdmitted          = session.ApprovalReceiptAdmitted
	AtomicCommitNotPublished         = fileutil.AtomicCommitNotPublished
	AtomicCommitPublishedUnconfirmed = fileutil.AtomicCommitPublishedUnconfirmed
	AtomicCommitCommitted            = fileutil.AtomicCommitCommitted
)

type SteerRequest = runtime.SteerRequest
type SteerResult = runtime.SteerResult
type RunResult = runtime.RunResult
type ProbeRequest = runtime.ProbeRequest
type ProbeResult = runtime.ProbeResult
type SessionState = session.State
type TaskBoard = session.TaskBoard
type SessionSummary = session.SessionSummary
type ContextReport = session.ContextReport

// Runner is the public core SDK facade. Experimental queue/delegation surfaces
// stay behind the CLI/internal layer until they are stabilized separately.
type Runner struct {
	core *runtime.CoreRunner
}

func New(cfg *config.Config) *Runner {
	return &Runner{core: runtime.NewCoreRunner(cfg)}
}

func (r *Runner) Start(ctx context.Context, req StartRequest) (RunResult, error) {
	return r.core.Start(ctx, req)
}

func (r *Runner) Continue(ctx context.Context, req ContinueRequest) (RunResult, error) {
	return r.core.Continue(ctx, req)
}

func (r *Runner) PrepareApprovalOperation(ctx context.Context, req ContinueRequest) (ApprovalPreparationResult, error) {
	return r.core.PrepareApprovalOperation(ctx, req)
}
func (r *Runner) LookupApprovalContinue(req ContinueRequest) (ApprovalReceiptLookup, error) {
	return r.core.LookupApprovalContinue(req)
}
func (r *Runner) ApprovalReceipt(sessionID, requestID string) (ApprovalReceiptLookup, error) {
	return r.core.ApprovalReceipt(sessionID, requestID)
}

func (r *Runner) PrepareApprovalContinue(ctx context.Context, req ContinueRequest) (*PreparedApproval, error) {
	return r.core.PrepareApprovalContinue(ctx, req)
}

func (r *Runner) RunPreparedApproval(ctx context.Context, prepared *PreparedApproval) (RunResult, error) {
	return r.core.RunPreparedApproval(ctx, prepared)
}

func (r *Runner) AbortPreparedApproval(prepared *PreparedApproval, cause error) error {
	return r.core.AbortPreparedApproval(prepared, cause)
}

func (r *Runner) Steer(ctx context.Context, req SteerRequest) (SteerResult, error) {
	return r.core.Steer(ctx, req)
}

func (r *Runner) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	return r.core.Probe(ctx, req)
}

func (r *Runner) Interrupt(sessionID string) error {
	return r.core.Interrupt(sessionID)
}

// Approval reads one coordinated approval snapshot for operator review.
func (r *Runner) Approval(sessionID string) (ApprovalSnapshot, error) {
	return r.core.Approval(sessionID)
}

func (r *Runner) State(sessionID string) (SessionState, error) {
	return r.core.State(sessionID)
}

func (r *Runner) Tasks(sessionID string) (TaskBoard, error) {
	return r.core.Tasks(sessionID)
}

func (r *Runner) List(limit int) ([]SessionSummary, error) {
	return r.core.List(limit)
}

func (r *Runner) Context(sessionID string) (ContextReport, error) {
	return r.core.Context(sessionID)
}
