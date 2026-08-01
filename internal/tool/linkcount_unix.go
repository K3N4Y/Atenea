//go:build unix

package tool

import (
	"os"
	"syscall"
)

func hasSingleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
