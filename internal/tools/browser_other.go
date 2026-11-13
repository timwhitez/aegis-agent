//go:build !linux

package tools

import (
	"errors"
	"os"
	"runtime"
)

// darwin and the BSDs size sockaddr_un.sun_path at 104 bytes including the
// NUL terminator. The browser adapter itself is gated by BrowserPlatformError.
const (
	browserSocketPathMax      = 103
	browserSocketPathPlatform = runtime.GOOS
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
