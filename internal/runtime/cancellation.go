package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go-cli-agent/internal/session"
)

const agentCancelRequestedReason = "agent_cancel_requested"

var activeSessionRunners = struct {
	sync.Mutex
	items map[string]*Runner
}{items: map[string]*Runner{}}

func activeSessionKey(store *session.Store, sessionID string) string {
	root := ""
	if store != nil {
		root = store.Root()
		if absolute, err := filepath.Abs(root); err == nil {
			root = absolute
		}
	}
	return root + "\x00" + strings.TrimSpace(sessionID)
}

func registerActiveSessionRunner(store *session.Store, sessionID string, runner *Runner) func() {
	key := activeSessionKey(store, sessionID)
	activeSessionRunners.Lock()
	activeSessionRunners.items[key] = runner
	activeSessionRunners.Unlock()
	return func() {
		activeSessionRunners.Lock()
		if activeSessionRunners.items[key] == runner {
			delete(activeSessionRunners.items, key)
		}
		activeSessionRunners.Unlock()
	}
}

func interruptRegisteredChild(store *session.Store, sessionID string) bool {
	key := activeSessionKey(store, sessionID)
	activeSessionRunners.Lock()
	runner := activeSessionRunners.items[key]
	activeSessionRunners.Unlock()
	if runner == nil {
		return false
	}
	// Registration is the in-process proof that this Runner still owns an active
	// execution path. A background-wait run persists awaiting_input while it is
	// still actively polling, so the public Interrupt status check (running only)
	// is intentionally bypassed here.
	runner.control.requestPauseWithReason(agentCancelRequestedReason)
	return true
}

func (r *Runner) watchSessionCancel(ctx context.Context, sessionID string) {
	poll := time.Duration(r.cfg.Runtime.Steer.PollIntervalMS) * time.Millisecond
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		request, err := r.store.LoadSessionCancel(sessionID)
		if err == nil && request.Status == session.CancelRequestStatusRequested {
			r.control.requestPauseWithReason(agentCancelRequestedReason)
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
