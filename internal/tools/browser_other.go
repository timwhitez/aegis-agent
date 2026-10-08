//go:build !linux

package tools

import (
	"errors"
	"os"
)

func (p *ownedBrowserProcess) wait(func() error) {
	p.mu.Lock()
	p.reaping = true
	p.mu.Unlock()
	p.waitErr = p.cmd.Wait()
	p.cleanupErr = errors.New("browser cleanup unsupported on this platform")
	close(p.done)
}

func browserExitSignal(*os.ProcessState) string { return "unknown" }
func browserProcessGroupCleanup(int) error {
	return errors.New("browser cleanup unsupported on this platform")
}
