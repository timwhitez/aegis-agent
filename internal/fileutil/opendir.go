package fileutil

import "golang.org/x/sys/unix"

// openDirNoSymlinkSingleOpen is the directory form of openNoSymlinkSingleOpen.
func openDirNoSymlinkSingleOpen(abs string) (int, bool) {
	return openNoSymlinkSingleOpen(abs, unix.O_RDONLY|unix.O_DIRECTORY)
}
