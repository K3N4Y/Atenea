package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func sandboxJoin(root, rel, toolName string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		rootAbs = filepath.Clean(root)
	}
	var abs string
	if filepath.IsAbs(rel) {
		// El modelo conoce el root por el system prompt y usa rutas absolutas de
		// forma natural: se aceptan si (limpias) caen dentro del root.
		abs = filepath.Clean(rel)
	} else {
		abs = filepath.Clean(filepath.Join(rootAbs, rel))
	}
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

// rejectMutableAlias rejects every symlink component below root and requires
// the final object to be a single-link regular file. This closes ordinary alias
// writes before read/validate; path-based Go APIs still cannot make the later
// lstat/read/rename sequence immune to a hostile concurrent directory swap.
func rejectMutableAlias(root, abs, rel, toolName string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	relFromRoot, err := filepath.Rel(rootAbs, abs)
	if err != nil || relFromRoot == "." || strings.HasPrefix(relFromRoot, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s: ruta mutable invalida: %s", toolName, rel)
	}
	current := rootAbs
	parts := strings.Split(relFromRoot, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: se rechaza alias symlink: %s", toolName, rel)
		}
		if i == len(parts)-1 && (!info.Mode().IsRegular() || !hasSingleLink(info)) {
			return fmt.Errorf("%s: se rechazan hardlinks y archivos no regulares: %s", toolName, rel)
		}
	}
	return nil
}

// rejectCreateAlias rejects symlink components for a creation path and
// hardlinked regular files at the final component. Creation must not follow a
// mutable in-workspace alias.
func rejectCreateAlias(root, abs, rel, toolName string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	relFromRoot, err := filepath.Rel(rootAbs, abs)
	if err != nil || relFromRoot == "." || strings.HasPrefix(relFromRoot, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s: ruta mutable invalida: %s", toolName, rel)
	}
	current := rootAbs
	parts := strings.Split(relFromRoot, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: se rechaza alias symlink: %s", toolName, rel)
		}
		if i == len(parts)-1 && info.Mode().IsRegular() && !hasSingleLink(info) {
			return fmt.Errorf("%s: se rechazan hardlinks: %s", toolName, rel)
		}
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
