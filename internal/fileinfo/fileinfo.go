// Package fileinfo reports a file's modification and creation times. Modification
// time is portable (os.FileInfo.ModTime); creation (birth) time is platform
// specific — captured natively on Windows and macOS, read via statx on Linux
// where the filesystem supports it, and otherwise reported as the zero time so
// callers can fall back to the modification time.
package fileinfo

import (
	"os"
	"time"
)

// Times returns a file's modification and creation times. created is the zero
// time when the platform or filesystem doesn't expose a birth time; callers
// should fall back to modified in that case.
func Times(path string) (modified, created time.Time, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return fi.ModTime(), birthTime(path, fi), nil
}
