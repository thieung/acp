#!/bin/sh
# Exercise install.sh against a local build: fresh install, upgrade, refusals, legacy and app links,
# bad checksum.
#   sh scripts/build-release.sh v0.0.0-dev && sh scripts/tests/test-install.sh
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
dist=$root/dist
[ -f "$dist/checksums.txt" ] || { echo "run scripts/build-release.sh first" >&2; exit 2; }
shell=${TEST_SHELL:-sh}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
install_acp() { # base [extra env...]
  base=$1
  shift
  env HOME="$work/home" ACP_INSTALL_DIR="$work/bin" ACP_NO_INIT=1 ACP_RELEASE_BASE="$base" "$@" \
    "$shell" "$root/install.sh" >"$work/out" 2>&1
}

mkdir -p "$work/home"
install_acp "file://$dist" || fail "fresh install: $(cat "$work/out")"
"$work/bin/acp" --version | grep -q 'acp v' || fail "installed binary does not run"

install_acp "file://$dist" || fail "reinstall over acp: $(cat "$work/out")"

install_acp "file://$dist" ACP_VERSION=v9.9.9 && fail "unknown version installed"
grep -q 'no acp archive' "$work/out" || fail "unknown version message: $(cat "$work/out")"

built=$(sed -n 's/.* acp_\([^ ]*\)_[a-z]*_[a-z0-9]*\.tar\.gz$/\1/p' "$dist/checksums.txt" | sed -n 1p)
install_acp "file://$dist" ACP_VERSION="$built" || fail "a version without the v prefix: $(cat "$work/out")"
install_acp "file://$dist" ACP_VERSION='v0.0.0-*' && fail "a version with a pattern character was accepted"
grep -q 'not a release tag' "$work/out" || fail "bad version message: $(cat "$work/out")"

rm "$work/bin/acp"
mkdir -p "$work/old/scripts"
printf '#!/usr/bin/env python3\n' >"$work/old/scripts/acp"
ln -s "$work/old/scripts/acp" "$work/bin/acp"
install_acp "file://$dist" || fail "legacy link: $(cat "$work/out")"
grep -q 'Python acp' "$work/out" || fail "legacy link not announced"
[ ! -L "$work/bin/acp" ] || fail "legacy link left in place"
[ -f "$work/old/scripts/acp" ] || fail "legacy target removed"

rm "$work/bin/acp"
helper="$work/Applications/acp.app/Contents/Helpers/acp"
mkdir -p "$(dirname "$helper")"
cp "$dist/checksums.txt" "$helper"
ln -s "$helper" "$work/bin/acp"
install_acp "file://$dist" && fail "replaced the acp that acp.app manages"
grep -q 'managed by acp.app' "$work/out" || fail "app link message: $(cat "$work/out")"
[ "$(readlink "$work/bin/acp")" = "$helper" ] || fail "app link changed"
[ "$(cat "$helper")" = "$(cat "$dist/checksums.txt")" ] || fail "app binary changed"

# The app was deleted: its link dangles and is replaced.
rm -rf "$work/Applications/acp.app"
install_acp "file://$dist" || fail "dangling app link: $(cat "$work/out")"
grep -q 'acp.app, which is gone' "$work/out" || fail "dangling app link not announced: $(cat "$work/out")"
[ ! -L "$work/bin/acp" ] || fail "dangling app link left in place"
"$work/bin/acp" --version | grep -q 'acp v' || fail "binary over a dangling app link does not run"

rm "$work/bin/acp"
ln -s /bin/sh "$work/bin/acp"
install_acp "file://$dist" && fail "replaced a foreign link"
[ "$(readlink "$work/bin/acp")" = /bin/sh ] || fail "foreign link changed"

rm "$work/bin/acp"
printf 'not acp\n' >"$work/bin/acp"
install_acp "file://$dist" && fail "replaced a foreign file"
[ "$(cat "$work/bin/acp")" = "not acp" ] || fail "foreign file changed"
rm "$work/bin/acp"

mkdir -p "$work/bad"
cp "$dist"/*.tar.gz "$work/bad/"
sed 's/^[0-9a-f]/0/' "$dist/checksums.txt" >"$work/bad/checksums.txt"
install_acp "file://$work/bad" && fail "installed with a bad checksum"
grep -q 'checksum mismatch' "$work/out" || fail "bad checksum message: $(cat "$work/out")"
[ ! -e "$work/bin/acp" ] || fail "bad checksum left a binary"

install_acp "http://example.com" && fail "accepted an http:// source"
grep -q 'only https:// and file://' "$work/out" || fail "http message: $(cat "$work/out")"

echo "install.sh: all checks passed ($shell)"
