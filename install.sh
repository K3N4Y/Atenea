#!/bin/sh

set -eu

repository=${ATENEA_REPOSITORY:-K3N4Y/Atenea}
download_base=${ATENEA_DOWNLOAD_BASE_URL:-https://github.com/$repository/releases/download}
version=${ATENEA_VERSION:-}
bin_dir=${ATENEA_INSTALL_DIR:-${HOME:?HOME is required}/.local/bin}

usage() {
    cat <<'EOF'
Install the Atenea terminal interface.

Usage: install.sh [--version VERSION] [--bin-dir DIRECTORY]

Options:
  --version VERSION    Install a specific release (for example, v0.1.0).
  --bin-dir DIRECTORY  Install into DIRECTORY (default: ~/.local/bin).
  -h, --help           Show this help.
EOF
}

fail() {
    printf 'install.sh: %s\n' "$1" >&2
    exit 1
}

need() {
    command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || fail "--version requires a value"
            version=$2
            shift 2
            ;;
        --bin-dir)
            [ "$#" -ge 2 ] || fail "--bin-dir requires a value"
            bin_dir=$2
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            fail "unknown argument: $1"
            ;;
    esac
done

need curl
need tar

case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$version" ]; then
    latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repository/releases/latest") || fail "could not resolve the latest release"
    version=${latest_url##*/}
fi

case "$version" in
    v*) tag=$version; release_version=${version#v} ;;
    *) tag=v$version; release_version=$version ;;
esac
[ -n "$release_version" ] || fail "invalid empty version"

archive="atenea_${release_version}_${os}_${arch}.tar.gz"
release_url="$download_base/$tag"
tmp_dir=$(mktemp -d 2>/dev/null || mktemp -d -t atenea-install) || fail "could not create a temporary directory"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

printf 'Downloading Atenea %s for %s/%s...\n' "$tag" "$os" "$arch"
curl -fL --retry 3 --proto '=https,file' --tlsv1.2 -o "$tmp_dir/$archive" "$release_url/$archive" || fail "could not download $archive"
curl -fL --retry 3 --proto '=https,file' --tlsv1.2 -o "$tmp_dir/checksums.txt" "$release_url/checksums.txt" || fail "could not download checksums.txt"

(
    cd "$tmp_dir"
    grep "  $archive\$" checksums.txt > expected-checksum || fail "release checksum for $archive is missing"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum -c expected-checksum >/dev/null
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 -c expected-checksum >/dev/null
    else
        fail "sha256sum or shasum is required"
    fi
) || fail "checksum verification failed"

tar -xzf "$tmp_dir/$archive" -C "$tmp_dir" atenea || fail "could not extract atenea"
mkdir -p "$bin_dir" || fail "could not create $bin_dir"
if command -v install >/dev/null 2>&1; then
    install -m 0755 "$tmp_dir/atenea" "$bin_dir/atenea"
else
    cp "$tmp_dir/atenea" "$bin_dir/atenea"
    chmod 0755 "$bin_dir/atenea"
fi

printf 'Atenea %s installed at %s/atenea\n' "$tag" "$bin_dir"
case ":${PATH:-}:" in
    *:"$bin_dir":*) ;;
    *) printf 'Add %s to your PATH to run atenea from any directory.\n' "$bin_dir" ;;
esac
