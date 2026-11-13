//go:build linux || !unix

package procutil

import "os"

// Getwd returns the process working directory. Linux getcwd(2) already fails
// with ENOENT once the working directory has been removed, so os.Getwd is
// authoritative here.
func Getwd() (string, error) {
	return os.Getwd()
}
