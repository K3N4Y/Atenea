//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package learning

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type fileLock struct {
	file *os.File
}

func lockFile(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &fileLock{file: file}, nil
}

func tryLockFile(path string) (*fileLock, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &fileLock{file: file}, true, nil
}

func (l *fileLock) unlock() error {
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	return errors.Join(unlockErr, l.file.Close())
}
