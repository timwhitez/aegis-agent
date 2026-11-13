//go:build unix

package procutil

import "path/filepath"

// Abs is filepath.Abs resolved against Getwd: an absolute path is only
// cleaned, and a relative path is joined to the current working directory.
// On Linux this is exactly filepath.Abs. On darwin and the BSDs, filepath.Abs
// joins a relative path to the stale path that getcwd(3) keeps returning after
// the working directory was removed, so creating that path would silently
// recreate the deleted directory (or write into whatever now occupies its old
// path). Abs instead fails there with the Linux "getwd: no such file or
// directory" error before the caller touches the filesystem. Use it whenever a
// relative path decides what to create, modify or trust.
func Abs(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	wd, err := Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, path), nil
}
