#!/bin/sh
# Install moa from GitHub Releases.
#
#   curl -fsSL https://letmoa.run/install.sh | sh
#
# Environment:
#   MOA_VERSION       install a specific version (e.g. v0.18.1) instead of latest
#   MOA_INSTALL_DIR   install directory (default: /usr/local/bin if writable, else ~/.local/bin)
set -eu

REPO="e-aleixandre/moa"
TMPDIR_MOA=""

die() {
	echo "install.sh: $1" >&2
	exit 1
}

cleanup() {
	[ -n "$TMPDIR_MOA" ] && rm -rf "$TMPDIR_MOA"
}
trap cleanup EXIT INT TERM

need() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

detect_platform() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	case "$os" in
	linux | darwin) ;;
	*) die "unsupported OS '$os'. Download a binary from https://github.com/$REPO/releases/latest" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch="amd64" ;;
	aarch64 | arm64) arch="arm64" ;;
	*) die "unsupported architecture '$(uname -m)'. Download a binary from https://github.com/$REPO/releases/latest" ;;
	esac
}

latest_version() {
	curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		grep -m1 '"tag_name"' |
		sed -e 's/.*"tag_name"[[:space:]]*:[[:space:]]*"//' -e 's/".*//'
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "no sha256 tool found (need sha256sum or shasum)"
	fi
}

install_dir() {
	if [ -n "${MOA_INSTALL_DIR:-}" ]; then
		mkdir -p "$MOA_INSTALL_DIR" || die "cannot create $MOA_INSTALL_DIR"
		echo "$MOA_INSTALL_DIR"
		return
	fi
	if [ -w /usr/local/bin ]; then
		echo /usr/local/bin
		return
	fi
	# No sudo, ever: fall back to a directory the user certainly owns.
	mkdir -p "$HOME/.local/bin" || die "cannot create $HOME/.local/bin"
	echo "$HOME/.local/bin"
}

need curl
need tar
detect_platform

version="${MOA_VERSION:-$(latest_version)}"
[ -n "$version" ] || die "could not resolve the latest version from the GitHub API"
version="v${version#v}"
plain="${version#v}"

archive="moa_${plain}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

TMPDIR_MOA=$(mktemp -d)
echo "Downloading $archive ..."
curl -fsSL "$base/$archive" -o "$TMPDIR_MOA/$archive" ||
	die "could not download $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$TMPDIR_MOA/checksums.txt" ||
	die "could not download $base/checksums.txt"

want=$(grep " \{1,\}\*\{0,1\}$archive\$" "$TMPDIR_MOA/checksums.txt" | cut -d' ' -f1)
[ -n "$want" ] || die "no checksum entry for $archive"
got=$(sha256_of "$TMPDIR_MOA/$archive")
[ "$got" = "$want" ] || die "checksum mismatch for $archive (got $got, want $want)"

tar -xzf "$TMPDIR_MOA/$archive" -C "$TMPDIR_MOA" moa || die "could not extract moa from $archive"

dir=$(install_dir)
target="$dir/moa"
old=""
if [ -x "$target" ]; then
	old=$("$target" version 2>/dev/null | head -1 || true)
fi

install -m 0755 "$TMPDIR_MOA/moa" "$target" 2>/dev/null ||
	{ cp "$TMPDIR_MOA/moa" "$target" && chmod 0755 "$target"; } ||
	die "cannot write to $dir — set MOA_INSTALL_DIR to a directory you can write to"

if [ -n "$old" ]; then
	echo "Updated: $old → moa $version"
else
	echo "Installed moa $version to $target"
fi

case ":$PATH:" in
*":$dir:"*) ;;
*) echo "Note: $dir is not in your PATH. Add it with: export PATH=\"$dir:\$PATH\"" ;;
esac

echo "Run 'moa --login anthropic' or 'moa --login openai' to authenticate, then 'moa'."
