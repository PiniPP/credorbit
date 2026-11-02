#!/bin/sh
# CredOrbit installer for macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/packaging/install.sh | sh
#
# Environment:
#   CREDORBIT_REPO      GitHub repository (default: PiniPP/credorbit)
#   CREDORBIT_VERSION   tag to install, e.g. v1.2.3 (default: latest release)
#   CREDORBIT_INSTALL   install directory (default: ~/.local/bin)
#   GITHUB_TOKEN        optional, raises GitHub API rate limits
#
# The download is verified against the release's checksums.txt (SHA-256).
# Nothing is installed with sudo; choose a writable CREDORBIT_INSTALL instead.
set -eu

REPO="${CREDORBIT_REPO:-PiniPP/credorbit}"
VERSION="${CREDORBIT_VERSION:-}"
INSTALL_DIR="${CREDORBIT_INSTALL:-$HOME/.local/bin}"
BINARY="credorbit"

say() { printf '%s\n' "credorbit-install: $*" >&2; }
die() { say "error: $*"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

fetch() { # fetch URL DEST
	if command -v curl >/dev/null 2>&1; then
		if [ -n "${GITHUB_TOKEN:-}" ]; then
			curl -fsSL --proto '=https' --tlsv1.2 -H "Authorization: Bearer $GITHUB_TOKEN" -o "$2" "$1"
		else
			curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
		fi
	elif command -v wget >/dev/null 2>&1; then
		if [ -n "${GITHUB_TOKEN:-}" ]; then
			wget -q --https-only --header "Authorization: Bearer $GITHUB_TOKEN" -O "$2" "$1"
		else
			wget -q --https-only -O "$2" "$1"
		fi
	else
		die "need curl or wget"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		die "need sha256sum, shasum or openssl to verify the download"
	fi
}

case "$REPO" in
	*/*) ;;
	*) die "CREDORBIT_REPO must look like owner/name (got '$REPO')" ;;
esac

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
	darwin | linux) ;;
	*) die "unsupported OS '$os' (use install.ps1 on Windows)" ;;
esac

arch="$(uname -m)"
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported architecture '$arch'" ;;
esac

need tar
need awk
need uname

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t credorbit)"
trap 'rm -rf "$tmp"' EXIT INT TERM

if [ -z "$VERSION" ]; then
	say "looking up the latest release of $REPO"
	fetch "https://api.github.com/repos/$REPO/releases/latest" "$tmp/latest.json"
	VERSION="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)"
	[ -n "$VERSION" ] || die "could not determine the latest release (set CREDORBIT_VERSION)"
fi
case "$VERSION" in
	v*) ;;
	*) VERSION="v$VERSION" ;;
esac
num="${VERSION#v}"

archive="${BINARY}_${num}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

say "downloading $archive ($VERSION)"
fetch "$base/$archive" "$tmp/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

expected="$(awk -v f="$archive" '$2 == f || $2 == "*"f {print $1}' "$tmp/checksums.txt" | head -n 1)"
[ -n "$expected" ] || die "$archive is not listed in checksums.txt"
actual="$(sha256 "$tmp/$archive")"
if [ "$expected" != "$actual" ]; then
	die "checksum mismatch for $archive (expected $expected, got $actual)"
fi
say "checksum verified"

tar -xzf "$tmp/$archive" -C "$tmp" "$BINARY" || die "archive does not contain $BINARY"

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
[ -w "$INSTALL_DIR" ] || die "$INSTALL_DIR is not writable; set CREDORBIT_INSTALL to a directory you own"
dest="$INSTALL_DIR/$BINARY"
cp "$tmp/$BINARY" "$dest.new"
chmod 0755 "$dest.new"
mv -f "$dest.new" "$dest"
say "installed $dest"

case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) say "note: $INSTALL_DIR is not on your PATH. Add it, e.g.: export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

"$dest" version 2>/dev/null || true
