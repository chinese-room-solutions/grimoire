//go:build !windows && !darwin && !linux

package fileinfo

import (
	"os"
	"time"
)

// birthTime reports no creation time on platforms without a portable birth-time
// source; the caller falls back to the modification time.
func birthTime(_ string, _ os.FileInfo) time.Time {
	return time.Time{}
}
