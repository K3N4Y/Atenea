package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigDir_UsesXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if want := filepath.Join(root, productDirectory); got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigDir_FallsBackToUserConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir() error = %v", err)
	}
	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if want := filepath.Join(root, productDirectory); got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestDataDir_UsesXDGDataHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error = %v", err)
	}
	if want := filepath.Join(root, productDirectory); got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestDataDir_FallsBackToPlatformDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")

	var root string
	var err error
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		root, err = os.UserConfigDir()
	} else {
		var home string
		home, err = os.UserHomeDir()
		root = filepath.Join(home, ".local", "share")
	}
	if err != nil {
		t.Fatalf("resolve platform data directory: %v", err)
	}

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error = %v", err)
	}
	if want := filepath.Join(root, productDirectory); got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestCacheDir_UsesXDGCacheHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)

	got, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}
	if want := filepath.Join(root, productDirectory); got != want {
		t.Fatalf("CacheDir() = %q, want %q", got, want)
	}
}

func TestCacheDir_FallsBackToUserCacheDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")

	root, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir() error = %v", err)
	}
	got, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}
	if want := filepath.Join(root, productDirectory); got != want {
		t.Fatalf("CacheDir() = %q, want %q", got, want)
	}
}

func TestFilesystemRootsAreDistinctWhenXDGRootsAreDistinct(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))

	config, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	data, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error = %v", err)
	}
	cache, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}

	if config == data || config == cache || data == cache {
		t.Fatalf("filesystem roots are not distinct: config=%q data=%q cache=%q", config, data, cache)
	}
}

func TestArtifactPathsUseTheirXDGRoots(t *testing.T) {
	base := t.TempDir()
	config := filepath.Join(base, "config")
	data := filepath.Join(base, "data")
	cache := filepath.Join(base, "cache")
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CACHE_HOME", cache)

	tests := []struct {
		name string
		path func() (string, error)
		want string
	}{
		{name: "database", path: DB, want: filepath.Join(data, productDirectory, databaseFile)},
		{name: "checkpoints", path: Checkpoints, want: filepath.Join(data, productDirectory, checkpointsDir)},
		{name: "credentials", path: Credentials, want: filepath.Join(config, productDirectory, credentialsFile)},
		{name: "providers", path: Providers, want: filepath.Join(config, productDirectory, providersFile)},
		{name: "models cache", path: ModelsCache, want: filepath.Join(cache, productDirectory, modelsCacheFile)},
		{name: "MCP config", path: MCPConfig, want: filepath.Join(config, productDirectory, mcpConfigFile)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.path()
			if err != nil {
				t.Fatalf("resolve path: %v", err)
			}
			if got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
		})
	}
}
