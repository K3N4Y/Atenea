// Package agents exposes Atenea's packaged subagent manifests to the runtime.
package agents

import "embed"

// Manifests contains every built-in subagent definition shipped from this directory.
//
//go:embed *.md
var Manifests embed.FS
