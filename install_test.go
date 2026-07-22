package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallScript_InstallsVerifiedReleaseBinary(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the POSIX installer supports Linux and macOS")
	}

	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	version := "1.2.3"
	tag := "v" + version
	arch := releaseArch(t)
	osName := runtime.GOOS
	archiveName := fmt.Sprintf("atenea_%s_%s_%s.tar.gz", version, osName, arch)

	fixture := t.TempDir()
	releaseDir := filepath.Join(fixture, tag)
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command(
		"go", "build", "-tags", "production",
		"-ldflags", "-X main.version="+tag+" -X main.commit=abc1234 -X main.buildDate=2026-07-21T12:00:00Z",
		"-o", binary, "./cmd/atenea",
	)
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build release fixture: %v\n%s", err, output)
	}
	archive := filepath.Join(releaseDir, archiveName)
	tar := exec.Command("tar", "-czf", archive, "-C", filepath.Dir(binary), filepath.Base(binary))
	if output, err := tar.CombinedOutput(); err != nil {
		t.Fatalf("create release fixture: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256(contents), archiveName)
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte(checksum), 0o644); err != nil {
		t.Fatal(err)
	}

	installDir := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command("sh", filepath.Join(root, "install.sh"), "--version", tag, "--bin-dir", installDir)
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"ATENEA_DOWNLOAD_BASE_URL=file://"+fixture,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install release: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Atenea "+tag+" installed") {
		t.Fatalf("installer output = %q, want success message", output)
	}

	installed := filepath.Join(installDir, "atenea")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("installed binary: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("installed binary mode = %v, want executable", info.Mode())
	}
	versionOutput, err := exec.Command(installed, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("run installed binary: %v\n%s", err, versionOutput)
	}
	wantVersion := "atenea v1.2.3 (commit abc1234, built 2026-07-21T12:00:00Z)\n"
	if string(versionOutput) != wantVersion {
		t.Fatalf("installed --version = %q, want %q", versionOutput, wantVersion)
	}
}

func TestInstallScript_RejectsReleaseWithInvalidChecksum(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the POSIX installer supports Linux and macOS")
	}

	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	archiveName := fmt.Sprintf("atenea_1.2.3_%s_%s.tar.gz", runtime.GOOS, releaseArch(t))
	fixture := t.TempDir()
	releaseDir := filepath.Join(fixture, "v1.2.3")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, archiveName), []byte("altered release"), 0o644); err != nil {
		t.Fatal(err)
	}
	badChecksum := strings.Repeat("0", 64) + "  " + archiveName + "\n"
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte(badChecksum), 0o644); err != nil {
		t.Fatal(err)
	}

	installDir := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command("sh", filepath.Join(root, "install.sh"), "--version", "v1.2.3", "--bin-dir", installDir)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "ATENEA_DOWNLOAD_BASE_URL=file://"+fixture)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("install altered release unexpectedly succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "checksum verification failed") {
		t.Fatalf("installer output = %q, want checksum failure", output)
	}
	if _, statErr := os.Stat(filepath.Join(installDir, "atenea")); !os.IsNotExist(statErr) {
		t.Fatalf("altered release installed a binary; stat error = %v", statErr)
	}
}

func releaseArch(t *testing.T) string {
	t.Helper()
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH
	default:
		t.Skipf("release architecture %s is not supported", runtime.GOARCH)
		return ""
	}
}
