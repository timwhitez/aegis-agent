//go:build !linux

package tools

import "os/exec"

func prepareCommandCancellation(cmd *exec.Cmd) {}
