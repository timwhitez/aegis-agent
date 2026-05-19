package runtime

import (
	"context"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

// CoreRunner exposes the stable runtime surface used by the web, CLI, and SDK adapters.
type CoreRunner struct {
	runner *Runner
}

func NewCoreRunner(cfg *config.Config) *CoreRunner {
	return &CoreRunner{runner: NewRunner(cfg)}
}

func (r *CoreRunner) Start(ctx context.Context, req StartRequest) (RunResult, error) {
	return r.runner.Start(ctx, req)
}

func (r *CoreRunner) Continue(ctx context.Context, req ContinueRequest) (RunResult, error) {
	return r.runner.Continue(ctx, req)
}

func (r *CoreRunner) Steer(ctx context.Context, req SteerRequest) (SteerResult, error) {
	return r.runner.Steer(ctx, req)
}

func (r *CoreRunner) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	return r.runner.Probe(ctx, req)
}

func (r *CoreRunner) Interrupt(sessionID string) error {
	return r.runner.Interrupt(sessionID)
}

func (r *CoreRunner) State(sessionID string) (session.State, error) {
	return r.runner.State(sessionID)
}

func (r *CoreRunner) Tasks(sessionID string) (session.TaskBoard, error) {
	return r.runner.Tasks(sessionID)
}

func (r *CoreRunner) List(limit int) ([]session.SessionSummary, error) {
	return r.runner.List(limit)
}

func (r *CoreRunner) Bus() *events.Bus {
	return r.runner.Bus()
}

// ExperimentalRunner keeps extension-only flows on an explicit facade.
type ExperimentalRunner struct {
	runner *Runner
}

func NewExperimentalRunner(cfg *config.Config) *ExperimentalRunner {
	return &ExperimentalRunner{runner: NewRunner(cfg)}
}

func (r *ExperimentalRunner) Store() *session.Store {
	return r.runner.Store()
}

func (r *ExperimentalRunner) Delegate(ctx context.Context, req DelegateRequest) (DelegateResult, error) {
	return r.runner.Delegate(ctx, req)
}

func (r *ExperimentalRunner) QueueSubmit(ctx context.Context, req QueueSubmitRequest) (session.QueueJob, error) {
	return r.runner.QueueSubmit(ctx, req)
}

func (r *ExperimentalRunner) QueueShow(jobID string) (session.QueueJob, error) {
	return r.runner.QueueShow(jobID)
}

func (r *ExperimentalRunner) QueueList(limit int) ([]session.QueueJob, error) {
	return r.runner.QueueList(limit)
}

func (r *ExperimentalRunner) ProcessNextJob(ctx context.Context) (session.QueueJob, bool, error) {
	return r.runner.ProcessNextJob(ctx)
}

// StoreView is a store-only facade for read-oriented experimental surfaces.
type StoreView struct {
	store *session.Store
}

func NewStoreView(cfg *config.Config) *StoreView {
	return &StoreView{store: newSessionStore(cfg)}
}

func (r *StoreView) Store() *session.Store {
	return r.store
}
