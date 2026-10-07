//go:build !linux

package tools

import (
	"errors"
	"os"
)

func browserExitSignal(*os.ProcessState) string { return "unknown" }
func browserProcessGroupCleanup(int) error {
	return errors.New("browser cleanup unsupported on this platform")
}
