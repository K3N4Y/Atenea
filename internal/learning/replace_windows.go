//go:build windows

package learning

import (
	"os"

	"golang.org/x/sys/windows"
)

func replaceFile(oldPath, newPath string) error {
	oldPtr, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPtr, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(oldPtr, newPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	return os.Chmod(newPath, 0o600)
}
