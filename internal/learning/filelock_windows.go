//go:build windows

package learning

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type fileLock struct {
	file *os.File
}

func lockFile(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
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
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &fileLock{file: file}, true, nil
}

func (l *fileLock) unlock() error {
	overlapped := new(windows.Overlapped)
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, overlapped)
	return errors.Join(unlockErr, l.file.Close())
}
