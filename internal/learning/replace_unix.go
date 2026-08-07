//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package learning

import "os"

func replaceFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
