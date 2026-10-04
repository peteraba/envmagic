//go:build unix

package internal

import (
	"os"
	"syscall"
)

// OwnerUID returns the uid that owns info, and false where the platform has none.
func OwnerUID(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}
