//go:build !unix

package tool

import "os"

// Unsupported platforms fail closed for existing mutable files.
func hasSingleLink(os.FileInfo) bool { return false }
