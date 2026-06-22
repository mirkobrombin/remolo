#!/usr/bin/env bash
# Cross-compile the remolo binaries for every supported platform, CGO-free.
# Output lands in dist/. Usage: ./scripts/build.sh [version]
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-dev}"
OUT="dist"
mkdir -p "$OUT"

# os/arch pairs to build for.
PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

# Each main package to build into a binary.
CMDS=(
  "cmd/remolo:remolo"
  "cmd/remolo-relay:remolo-relay"
  "cmd/remolo-rendezvous:remolo-rendezvous"
)

for platform in "${PLATFORMS[@]}"; do
  GOOS="${platform%/*}"
  GOARCH="${platform#*/}"
  for entry in "${CMDS[@]}"; do
    pkg="${entry%:*}"
    name="${entry#*:}"
    ext=""
    if [ "$GOOS" = "windows" ]; then ext=".exe"; fi
    bin="$OUT/${name}-${GOOS}-${GOARCH}${ext}"
    echo "building $bin"
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -trimpath \
        -ldflags "-s -w -X github.com/mirkobrombin/remolo/internal/session.Version=$VERSION" \
        -o "$bin" "./$pkg"
  done
done

echo "done. artifacts in $OUT/"
ls -la "$OUT"
