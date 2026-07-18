package providerconfig

import (
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path via a synced temp file plus rename, so a
// crash never leaves a half-written file behind. Permissions are private by
// design — 0600 file inside a 0700 directory — because everything this package
// persists is either configuration or secrets. Shared by providers.json,
// models-cache.json, and credentials.json.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
