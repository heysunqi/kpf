#!/usr/bin/env bash
# Build cross-platform binaries for kpf release assets.
#
# Outputs to ./dist/kpf-<version>-<goos>-<goarch>.tar.gz, each containing
# the binary plus README.md and LICENSE placeholder. A SHASUMS256.txt is
# emitted at the end for downstream verification.
#
# Usage: ./scripts/build-release.sh [version]
#   version defaults to the value of `kpf version` (cmd/kpf/main.go).

set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(grep -oE 'const version = "[^"]+"' cmd/kpf/main.go | head -1 | sed 's/.*"\(.*\)".*/\1/')}"
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo dev)
DATE=$(date -u +"%Y-%m-%d")
OUT="dist"

LDFLAGS="-s -w"

mkdir -p "$OUT/build"

# (goos goarch, ext)
TARGETS=(
  "linux amd64 "
  "linux arm64 "
  "darwin amd64 "
  "darwin arm64 "
)

for triple in "${TARGETS[@]}"; do
  read -r GOOS GOARCH EXT <<<"$triple"
  BIN="kpf-${VERSION}-${GOOS}-${GOARCH}.tar.gz"
  echo "==> building ${BIN%.tar.gz}"
  GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=0 \
    go build -trimpath -ldflags="$LDFLAGS" \
      -o "$OUT/build/kpf$EXT" ./cmd/kpf

  # Stage an archive staging dir with the binary renamed to plain `kpf`
  # so users can just untar and run `./kpf`. Adding README + LICENSE.
  STAGE="$OUT/build/kpf-${VERSION}-${GOOS}-${GOARCH}"
  mkdir -p "$STAGE"
  mv "$OUT/build/kpf$EXT" "$STAGE/kpf$EXT"
  # README excerpt (first 60 lines, sufficient for offline install instructions)
  {
    echo "# kpf ${VERSION} (${GOOS}/${GOARCH})"
    echo
    echo "Pre-built binary for kpf ${VERSION} (commit ${COMMIT}, ${DATE})."
    echo
    echo "## Install"
    echo
    echo '```sh'
    echo "tar -xzf kpf-${VERSION}-${GOOS}-${GOARCH}.tar.gz"
    echo "sudo mv kpf-${VERSION}-${GOOS}-${GOARCH}/kpf /usr/local/bin/kpf"
    echo '```'
    echo
    echo "Or:"
    echo
    echo '```sh'
    echo "sudo install -m 0755 kpf-${VERSION}-${GOOS}-${GOARCH}/kpf /usr/local/bin/kpf"
    echo '```'
    echo
    echo "## Verify"
    echo
    echo '```sh'
    echo "kpf version"
    echo '```'
    echo
    echo "Checksums: see \`kpf-${VERSION}-SHASUMS256.txt\` on the release page."
    echo
    echo "Full docs: https://github.com/heysunqi/kpf"
  } > "$STAGE/README.txt"
  cp README.md "$STAGE/README.md"

  tar -C "$OUT/build" -czf "$OUT/$BIN" "kpf-${VERSION}-${GOOS}-${GOARCH}"
  rm -rf "$STAGE"
done

# shasums
( cd "$OUT" && shasum -a 256 kpf-${VERSION}-*.tar.gz > kpf-${VERSION}-SHASUMS256.txt )

echo
echo "Artifacts ready in $OUT/:"
ls -lh "$OUT"/kpf-${VERSION}-*.tar.gz "$OUT"/kpf-${VERSION}-SHASUMS256.txt 2>/dev/null \
  | awk '{printf "  %-60s %s\n", $NF, $5}'
