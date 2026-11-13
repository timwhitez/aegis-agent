//go:build darwin

package webconsole

import "aegis-agent/internal/procutil"

// platformProcessAlive reports false only when the kernel proves the PID is
// unused or its process already exited (zombie). Permission and lookup errors
// keep the process alive.
func platformProcessAlive(pid int) bool {
	return !procutil.ProcessExited(pid)
}
