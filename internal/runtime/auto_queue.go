package runtime

import (
	"context"
	"sync"
	"time"
)

type autoQueueLease struct {
	mu     sync.Mutex
	refs   int
	cancel context.CancelFunc
	done   chan struct{}
}

var autoQueueWorkers sync.Map

func (r *Runner) startAutoQueueWorker() func() {
	if !r.cfg.Runtime.Queue.AutoWorker {
		return func() {}
	}
	root := r.store.Root()
	value, _ := autoQueueWorkers.LoadOrStore(root, &autoQueueLease{})
	lease := value.(*autoQueueLease)

	lease.mu.Lock()
	if lease.refs == 0 {
		workerCtx, cancel := context.WithCancel(context.Background())
		lease.cancel = cancel
		lease.done = make(chan struct{})
		go func(done chan struct{}) {
			defer close(done)
			r.runAutoQueueWorker(workerCtx)
		}(lease.done)
	}
	lease.refs++
	lease.mu.Unlock()

	released := false
	return func() {
		lease.mu.Lock()
		if released {
			lease.mu.Unlock()
			return
		}
		released = true
		lease.refs--
		if lease.refs > 0 {
			lease.mu.Unlock()
			return
		}
		cancel := lease.cancel
		done := lease.done
		lease.cancel = nil
		lease.done = nil
		autoQueueWorkers.Delete(root)
		lease.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
	}
}

func (r *Runner) runAutoQueueWorker(ctx context.Context) {
	poll := time.Duration(r.cfg.Runtime.Queue.PollIntervalMS) * time.Millisecond
	if poll <= 0 {
		poll = time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		job, ok, err := r.ProcessNextJob(ctx)
		if err != nil {
			timer.Reset(poll)
			continue
		}
		if ok && job.ID != "" {
			timer.Reset(0)
			continue
		}
		timer.Reset(poll)
	}
}
