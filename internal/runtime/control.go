package runtime

import (
	"context"
	"sync"
)

type runControl struct {
	mu             sync.Mutex
	cancel         context.CancelFunc
	pauseRequested bool
	pauseReason    string
	steerRequested bool
}

func (c *runControl) setCancel(cancel context.CancelFunc) {
	c.mu.Lock()
	c.cancel = cancel
	shouldCancel := c.pauseRequested || c.steerRequested
	c.mu.Unlock()
	if shouldCancel && cancel != nil {
		cancel()
	}
}

func (c *runControl) clearCancel(cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = cancel
	c.cancel = nil
}

//lint:ignore U1000 requestPause is exercised by in-package interruption tests.
func (c *runControl) requestPause() {
	c.requestPauseWithReason("keyboard_interrupt")
}

func (c *runControl) requestPauseWithReason(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pauseRequested = true
	c.pauseReason = reason
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *runControl) requestSteerInterrupt() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steerRequested = true
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *runControl) consumePause() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.pauseRequested
	c.pauseRequested = false
	return value
}

func (c *runControl) takePauseReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	reason := c.pauseReason
	c.pauseReason = ""
	if reason == "" {
		return "keyboard_interrupt"
	}
	return reason
}

func (c *runControl) consumeSteerInterrupt() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.steerRequested
	c.steerRequested = false
	return value
}
