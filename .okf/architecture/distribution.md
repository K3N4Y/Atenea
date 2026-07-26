---
updated_at: 2026-07-26
summary: Release and installation architecture for the Atenea command-line binary — the terminal interface and the headless CLI ship as one artifact.
---

# CLI distribution

The `atenea` binary is distributed as precompiled GitHub Release archives for Linux
and macOS on `amd64` and `arm64`. The desktop Wails application has a separate
delivery lifecycle and is not part of these archives.

One artifact carries both of the binary's surfaces: a bare `atenea` is the
[terminal interface](tui.md) and `atenea run` is the [headless CLI](headless-cli.md).
There is nothing to choose at install time and no second download for CI — which is
the point of shipping them together: an integrator installs the same executable a
user runs, so what a pipeline drives is what a person can reproduce by hand.

## Release contract

GoReleaser is the single source of truth for the release matrix, archive names,
production build tag, linker metadata, and checksum manifest. A tag such as
`v0.1.0` produces:

```text
atenea_0.1.0_linux_amd64.tar.gz
atenea_0.1.0_linux_arm64.tar.gz
atenea_0.1.0_darwin_amd64.tar.gz
atenea_0.1.0_darwin_arm64.tar.gz
checksums.txt
```

Release binaries use the `production` build tag so they never load credentials
from a workspace `.env`. GoReleaser injects the tag, commit, and build date;
`atenea --version` and `atenea version` expose those values — the same string, one
being an alias of the other — without initializing either surface or any persistent
state.

`.github/workflows/release.yml` publishes these artifacts only for `v*` tags.
The normal CI workflow also runs a snapshot release, which exercises every
target and the complete archive/checksum configuration without publishing.

## Installer contract

`install.sh` is the public installation interface. It detects the supported OS
and architecture, resolves the latest release unless a version is supplied,
downloads the matching archive and shared checksum manifest, verifies SHA-256,
and only then installs the executable. The default destination is
`~/.local/bin`; `--bin-dir` selects another user-owned prefix, so installation
does not require root privileges.

The installer accepts `--version` for reproducible installs. Re-running it is
the update path, and deleting the executable is the uninstall path. Tests use
`ATENEA_DOWNLOAD_BASE_URL` to substitute a local release fixture while crossing
the same process-level installer seam used by a real installation.
