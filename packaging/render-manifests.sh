#!/bin/sh
# Render the winget and scoop manifest templates for a published release.
#
#   packaging/render-manifests.sh <version-without-v> <checksums.txt> [outdir]
#
# CREDORBIT_REPO selects the GitHub repository (default credorbit/credorbit).
# GoReleaser already publishes a scoop manifest when a bucket token is set;
# these templates are for manual submission (e.g. winget-pkgs pull requests).
set -eu

[ $# -ge 2 ] || { echo "usage: $0 <version> <checksums.txt> [outdir]" >&2; exit 2; }
VERSION="${1#v}"
SUMS="$2"
OUT="${3:-dist/manifests}"
REPO="${CREDORBIT_REPO:-credorbit/credorbit}"
HERE="$(cd "$(dirname "$0")" && pwd)"

sum_for() {
	awk -v f="$1" '$2 == f || $2 == "*"f {print $1}' "$SUMS" | head -n 1
}
AMD64="$(sum_for "credorbit_${VERSION}_windows_amd64.zip")"
ARM64="$(sum_for "credorbit_${VERSION}_windows_arm64.zip")"
[ -n "$AMD64" ] && [ -n "$ARM64" ] || { echo "windows archives missing from $SUMS" >&2; exit 1; }
DATE="$(date -u +%Y-%m-%d)"

render() { # template output
	sed -e "s|\${VERSION}|$VERSION|g" \
		-e "s|\${REPO}|$REPO|g" \
		-e "s|\${RELEASE_DATE}|$DATE|g" \
		-e "s|\${SHA256_WINDOWS_AMD64}|$AMD64|g" \
		-e "s|\${SHA256_WINDOWS_ARM64}|$ARM64|g" \
		"$1" > "$2"
	echo "wrote $2"
}

mkdir -p "$OUT/winget/$VERSION" "$OUT/scoop"
for t in "$HERE"/winget/*.tmpl; do
	render "$t" "$OUT/winget/$VERSION/$(basename "$t" .tmpl)"
done
render "$HERE/scoop/credorbit.json.tmpl" "$OUT/scoop/credorbit.json"
