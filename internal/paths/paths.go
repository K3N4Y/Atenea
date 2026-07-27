// Package paths owns the product's user filesystem roots.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const productDirectory = "atenea"

// ConfigDir returns the directory for user-specific configuration.
func ConfigDir() (string, error) {
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		return filepath.Join(root, productDirectory), nil
	}

	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, productDirectory), nil
}

// DataDir returns the directory for durable user-specific data.
func DataDir() (string, error) {
	if root := os.Getenv("XDG_DATA_HOME"); root != "" {
		return filepath.Join(root, productDirectory), nil
	}

	root, err := userDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, productDirectory), nil
}

// CacheDir returns the directory for disposable user-specific data.
func CacheDir() (string, error) {
	if root := os.Getenv("XDG_CACHE_HOME"); root != "" {
		return filepath.Join(root, productDirectory), nil
	}

	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, productDirectory), nil
}

func userDataDir() (string, error) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	}
	return os.UserConfigDir()
}
