package fileinfo

import (
	"os"
	"syscall"
	"time"
)

// birthTime returns the file's creation time from the Win32 file attributes,
// which NTFS records reliably. The zero time is returned if the underlying data
// isn't a Win32FileAttributeData (shouldn't happen on Windows).
func birthTime(_ string, fi os.FileInfo) time.Time {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.CreationTime.Nanoseconds())
	}
	return time.Time{}
}
