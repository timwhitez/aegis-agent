//go:build !linux

package procutil

import "os/exec"

func PrepareCommandCancellation(cmd *exec.Cmd) {}
