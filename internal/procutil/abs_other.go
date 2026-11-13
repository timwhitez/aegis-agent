//go:build !unix

package procutil

import "path/filepath"

// Abs is filepath.Abs on hosts without Unix working-directory semantics.
func Abs(path string) (string, error) {
	return filepath.Abs(path)
}
