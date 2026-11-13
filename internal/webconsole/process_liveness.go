package webconsole

import "os"

// hostProcessAlive is conservative: it reports false only when the host proves
// the PID has no live process (process_liveness_procfs.go on Linux,
// process_liveness_darwin.go on macOS).
func hostProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	return platformProcessAlive(pid)
}
