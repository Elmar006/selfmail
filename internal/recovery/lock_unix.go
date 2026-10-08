//go:build !windows

package recovery

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLock(f *os.File, exclusive bool) (bool, error) {
	op := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		op = unix.LOCK_EX | unix.LOCK_NB
	}
	e := unix.Flock(int(f.Fd()), op)
	if errors.Is(e, unix.EAGAIN) || errors.Is(e, unix.EWOULDBLOCK) {
		return false, nil
	}
	return e == nil, e
}
func unlock(f *os.File) { unix.Flock(int(f.Fd()), unix.LOCK_UN) }
