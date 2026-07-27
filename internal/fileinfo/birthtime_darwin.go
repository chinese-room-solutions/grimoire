package fileinfo

import (
	"os"
	"syscall"
	"time"
)

// birthTime returns the file's creation time from the macOS stat birthtimespec,
// which APFS/HFS+ record. The zero time is returned if the underlying data isn't
// a *syscall.Stat_t.
func birthTime(_ string, fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	}
	return time.Time{}
}
