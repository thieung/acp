#!/bin/sh
# Build the acp release archives for macOS and Linux (amd64, arm64) into dist/, with checksums.
#   scripts/build-release.sh v0.1.0-beta.1
set -eu

tag=${1:?usage: scripts/build-release.sh TAG}
case $tag in
  v[0-9]*) ;;
  *) echo "build-release: tag must look like v1.2.3 or v1.2.3-beta.1, got $tag" >&2; exit 2 ;;
esac
version=${tag#v}
root=$(cd "$(dirname "$0")/.." && pwd)
dist=$root/dist
rm -rf "$dist"
mkdir -p "$dist"

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name=acp_${version}_${os}_${arch}
  work=$dist/$name
  mkdir -p "$work"
  (cd "$root" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X github.com/thieung/acp/internal/commands.Version=$tag" \
    -o "$work/acp" ./cmd/acp)
  tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
  rm -rf "$work"
done

cd "$dist"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum ./*.tar.gz | sed 's| \./| |' > checksums.txt
else
  shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt
fi
cat checksums.txt
