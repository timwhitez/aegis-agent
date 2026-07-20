package agent

import (
	"context"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/runtime"
	"go-cli-agent/internal/session"
)

type StartRequest = runtime.StartRequest
type ContinueRequest = runtime.ContinueRequest
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

func (r *Runner) Steer(ctx context.Context, req SteerRequest) (SteerResult, error) {
	return r.core.Steer(ctx, req)
}

func (r *Runner) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	return r.core.Probe(ctx, req)
}

func (r *Runner) Interrupt(sessionID string) error {
	return r.core.Interrupt(sessionID)
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
