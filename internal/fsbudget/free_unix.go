//go:build !windows

package fsbudget

import (
	"golang.org/x/sys/unix"
)

func Free(path string) (uint64, error) {
	var stat unix.Statfs_t
	if e := unix.Statfs(path, &stat); e != nil {
		return 0, e
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
