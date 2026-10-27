#!/bin/sh
# Install acp from dist/ on a machine without Python, then run `acp init` and `acp scan`.
# PATH holds only the tools install.sh and acp need, so a hidden dependency fails here.
#   sh scripts/build-release.sh v0.0.0-dev && sh scripts/tests/install-smoke.sh
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/home"
for tool in sh curl tar gzip git uname mktemp rm mkdir cp mv chmod sed grep readlink cat env \
  sha256sum shasum dirname; do
  path=$(command -v "$tool" 2>/dev/null) || continue
  ln -s "$path" "$work/bin/$tool"
done

# The inner script expands its own variables.
# shellcheck disable=SC2016
env -i HOME="$work/home" PATH="$work/bin:$work/home/.local/bin" sh -c '
  set -eu
  if command -v python3 >/dev/null 2>&1; then echo "python3 is on the smoke PATH" >&2; exit 1; fi
  ACP_RELEASE_BASE="file://$1/dist" sh "$1/install.sh"
  acp scan --json >"$HOME/scan.json"
  grep -q "\"kind\": \"acp.scan\"" "$HOME/scan.json"
  test -f "$HOME/agent-control-plane/projects.local.json"
' smoke "$root"
echo "install smoke: ok"
