package webconsole

import (
	"context"
	"time"

	"aegis-agent/internal/session"
)

const (
	defaultReaperInterval    = time.Minute
	minReaperInterval        = 5 * time.Second
	defaultLeaseStaleAfter   = session.QueueRunningStaleAfter
	queueReaperPauseReason   = "stale_owner_reconciled"
	queueReaperStartupJitter = 2 * time.Second
)

// startQueueReaper launches a background loop that reclaims orphaned queue jobs
// (owner process gone or lease stale) and reconciles zombie running sessions
// left behind by a previous process. This is what prevents a parent session
// from staying parked forever on child work whose worker has died — e.g. after a
// web service restart. It runs an immediate pass on startup, then on an
// interval. A non-positive reaper_interval_ms disables the loop.
func (s *Service) startQueueReaper() {
	interval := s.reaperInterval()
	if interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.reaperCancel = cancel
	s.reaperDone = make(chan struct{})
	go func() {
		defer close(s.reaperDone)
		// Small startup delay so an immediate restart does not race the just-died
		// process's own lease writes before its /proc entry disappears.
		select {
		case <-ctx.Done():
			return
		case <-time.After(queueReaperStartupJitter):
		}
		s.runReaperPass()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runReaperPass()
			}
		}
	}()
}

func (s *Service) stopQueueReaper() {
	if s.reaperCancel == nil {
		return
	}
	s.reaperCancel()
	if s.reaperDone != nil {
		<-s.reaperDone
	}
	s.reaperCancel = nil
	s.reaperDone = nil
}

// runReaperPass performs one reclamation sweep. Errors are swallowed (logged via
// events where applicable) so a transient store error never kills the loop.
func (s *Service) runReaperPass() {
	_, _ = s.store.ReapStaleQueueJobs(s.leaseStaleAfter())
	// Zombie running sessions (status=running with no live owning process) are
	// reconciled to paused via the existing self-filtering helper, which only
	// acts when the recorded owner is known dead/stale.
	_ = s.reconcileAllStaleRunningSessions()
}

// reconcileAllStaleRunningSessions sweeps every session recorded as running,
// letting reconcileStaleRunningSession self-filter to those whose owner is
// dead/stale. It deliberately uses ListRunningSessionIDs instead of ListPage:
// the reaper drops every non-running entry anyway, and ListPage additionally
// expands each session's goal/planmode snapshot, so a full page costs roughly
// four synchronous file reads per session for facts this sweep never uses.
func (s *Service) reconcileAllStaleRunningSessions() error {
	ids, err := s.store.ListRunningSessionIDs()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.reconcileStaleRunningSession(id, queueReaperPauseReason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reaperInterval() time.Duration {
	ms := s.cfg.Runtime.Queue.ReaperIntervalMS
	if ms < 0 {
		return 0
	}
	if ms == 0 {
		return defaultReaperInterval
	}
	interval := time.Duration(ms) * time.Millisecond
	if interval < minReaperInterval {
		return minReaperInterval
	}
	return interval
}

func (s *Service) leaseStaleAfter() time.Duration {
	sec := s.cfg.Runtime.Queue.LeaseStaleAfterSec
	if sec <= 0 {
		return defaultLeaseStaleAfter
	}
	return time.Duration(sec) * time.Second
}
