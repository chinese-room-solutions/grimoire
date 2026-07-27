package fileinfo

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// birthTime returns the file's creation time via statx, which modern Linux
// kernels expose on filesystems that record it (ext4, btrfs, xfs). The zero time
// is returned when the kernel or filesystem doesn't provide one, so the caller
// falls back to the modification time. The path is needed because Linux's
// os.FileInfo (*syscall.Stat_t) has no birth-time field.
func birthTime(path string, _ os.FileInfo) time.Time {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME, &stx); err != nil {
		return time.Time{}
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return time.Time{} // kernel/filesystem didn't supply it.
	}
	return time.Unix(stx.Btime.Sec, int64(stx.Btime.Nsec))
}
