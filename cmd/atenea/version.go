package main

import "fmt"

// These values are injected into release binaries by GoReleaser. Development
// builds remain identifiable without requiring special build flags.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func versionString() string {
	return fmt.Sprintf("atenea %s (commit %s, built %s)", version, commit, buildDate)
}
