package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func sandboxJoin(root, rel, toolName string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s: ruta fuera del workspace: %s", toolName, rel)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		rootAbs = filepath.Clean(root)
	}
	abs := filepath.Clean(filepath.Join(rootAbs, rel))
	if !insideRoot(rootAbs, abs) {
		return "", fmt.Errorf("%s: ruta fuera del workspace: %s", toolName, rel)
	}
	return abs, nil
}

func rejectRealPathOutside(root, abs, rel, toolName string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	targetReal, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil
	}
	if !insideRoot(rootReal, targetReal) {
		return fmt.Errorf("%s: ruta fuera del workspace: %s", toolName, rel)
	}
	return nil
}

func rejectRealParentOutside(root, abs, rel, toolName string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	parent, err := nearestExistingParent(filepath.Dir(abs))
	if err != nil {
		return err
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	if !insideRoot(rootReal, parentReal) {
		return fmt.Errorf("%s: ruta fuera del workspace: %s", toolName, rel)
	}
	return nil
}

func nearestExistingParent(path string) (string, error) {
	for {
		if _, err := os.Lstat(path); err == nil {
			return path, nil
		}
		next := filepath.Dir(path)
		if next == path {
			return "", os.ErrNotExist
		}
		path = next
	}
}

func insideRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
