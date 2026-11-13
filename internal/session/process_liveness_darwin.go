//go:build darwin

package session

import "aegis-agent/internal/procutil"

// platformProcessIdentity uses the darwin identity shared with run.sh,
// "darwin:<kern.bootsessionuuid>:<p_starttime seconds>" (see
// procutil.ProcessIdentity); a mismatch means the PID now belongs to another
// process instance.
func platformProcessIdentity(pid int) (string, bool) {
	identity, err := procutil.ProcessIdentity(pid)
	if err != nil {
		return "", false
	}
	return identity, true
}

// platformProcessAlive reports false only when the kernel proves the PID is
// unused or its process already exited (zombie). Permission and lookup errors
// keep the owner alive.
func platformProcessAlive(pid int) bool {
	return !procutil.ProcessExited(pid)
}
