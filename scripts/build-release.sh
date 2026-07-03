#!/usr/bin/env bash
# build-release.sh - cross-compile bakku release binaries for all supported
# platforms into dist/.
#
# Usage:
#   scripts/build-release.sh [version]
#
# `version` defaults to the output of `git describe --tags --always --dirty`
# if git is available and the tree is a git repo, otherwise "dev".
#
# Produces, under dist/:
#   bakku-<version>-darwin-amd64
#   bakku-<version>-darwin-arm64
#   bakku-<version>-linux-amd64
#   bakku-<version>-linux-arm64
#   bakku-<version>-windows-amd64.exe
#   bakku-<version>-windows-arm64.exe
# plus a bakku-<version>-checksums.txt (sha256) alongside them.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
  else
    VERSION="dev"
  fi
fi

COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

DIST_DIR="dist"
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"

# GOOS/GOARCH targets. Windows/arm64 and darwin/arm64 are supported by the Go
# toolchain as of Go 1.26; all six are CGO-free pure-Go builds.
TARGETS=(
  "darwin  amd64"
  "darwin  arm64"
  "linux   amd64"
  "linux   arm64"
  "windows amd64"
  "windows arm64"
)

echo "Building bakku ${VERSION} (commit ${COMMIT}, ${DATE})"

for target in "${TARGETS[@]}"; do
  read -r GOOS GOARCH <<<"$target"
  ext=""
  if [ "$GOOS" = "windows" ]; then
    ext=".exe"
  fi
  out="${DIST_DIR}/bakku-${VERSION}-${GOOS}-${GOARCH}${ext}"
  echo "  -> ${out}"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" ./cmd/bakku
done

echo "Writing checksums"
(
  cd "$DIST_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum bakku-* >"bakku-${VERSION}-checksums.txt"
  else
    shasum -a 256 bakku-* >"bakku-${VERSION}-checksums.txt"
  fi
)

echo "Done. Artifacts in ${DIST_DIR}/:"
ls -la "$DIST_DIR"
