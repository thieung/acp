#!/bin/sh
# Install acp from a GitHub release, then run `acp init`.
#
#   gh release download <tag> -R thieung/acp -p install.sh -O - | sh
#   curl -fsSL https://github.com/thieung/acp/releases/download/<tag>/install.sh | sh
#
# gh works while the repository is private; curl needs it public.
#
# ACP_VERSION       release tag to install (default: the newest release, prereleases included)
# ACP_INSTALL_DIR   where acp goes (default: ~/.local/bin)
# ACP_RELEASE_BASE  https:// or file:// folder holding checksums.txt and the archives
# ACP_NO_INIT=1     do not run `acp init` afterwards
set -eu

repo=thieung/acp
marker=ACP-BINARY-v1

say() { printf 'acp install: %s\n' "$*" >&2; }
die() { say "$*"; exit 1; }

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported system $(uname -s); acp ships for macOS and Linux" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m); acp ships for amd64 and arm64" ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fetch() { # url dest
  case $1 in
    file://*) cp "${1#file://}" "$2" ;;
    https://*) curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL -o "$2" "$1" ;;
    *) die "refusing $1: only https:// and file:// sources" ;;
  esac
}

version=${ACP_VERSION:-}
want="acp_[^ ]*_${os}_${arch}\.tar\.gz"
if [ -n "$version" ]; then
  case $version in
    *[!A-Za-z0-9.-]*) die "ACP_VERSION=$version is not a release tag (like v0.1.0 or v0.1.0-beta.1)" ;;
  esac
  # Release tags carry a v; the archive names do not.
  version=v${version#v}
  want="acp_$(printf '%s' "${version#v}" | sed 's/\./\\./g')_${os}_${arch}\.tar\.gz"
fi

if [ -n "${ACP_RELEASE_BASE:-}" ]; then
  base=${ACP_RELEASE_BASE%/}
  fetch "$base/checksums.txt" "$tmp/checksums.txt"
  get() { fetch "$base/$1" "$tmp/$1"; }
elif command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  # gh also works while the repository is private.
  if [ -z "$version" ]; then
    version=$(gh release list -R "$repo" --exclude-drafts --limit 1 --json tagName -q '.[0].tagName')
    [ -n "$version" ] || die "no release found in $repo"
  fi
  gh release download "$version" -R "$repo" -p checksums.txt -D "$tmp"
  get() { gh release download "$version" -R "$repo" -p "$1" -D "$tmp"; }
else
  if [ -z "$version" ]; then
    fetch "https://api.github.com/repos/$repo/releases?per_page=1" "$tmp/releases.json"
    version=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp/releases.json" | sed -n 1p)
    [ -n "$version" ] || die "no release found in $repo"
  fi
  base=https://github.com/$repo/releases/download/$version
  fetch "$base/checksums.txt" "$tmp/checksums.txt"
  get() { fetch "$base/$1" "$tmp/$1"; }
fi

line=$(grep " $want\$" "$tmp/checksums.txt" | sed -n 1p) || true
[ -n "$line" ] || die "no acp archive for $os/$arch${version:+ in $version}"
sum=${line%% *}
archive=${line##* }
get "$archive"

if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$archive")
else
  got=$(shasum -a 256 "$tmp/$archive")
fi
[ "${got%% *}" = "$sum" ] || die "checksum mismatch for $archive; nothing installed"

tar -xzf "$tmp/$archive" -C "$tmp"
new=$tmp/${archive%.tar.gz}/acp
LC_ALL=C grep -aq "$marker" "$new" || die "$archive does not hold an acp binary; nothing installed"

dir=${ACP_INSTALL_DIR:-$HOME/.local/bin}
dest=$dir/acp
mkdir -p "$dir"
if [ -L "$dest" ]; then
  old=$(readlink "$dest")
  case $old in
    */scripts/acp) say "replacing the link to the Python acp ($old)" ;;
    */acp.app/Contents/Helpers/acp)
      # A link into a deleted app leads nowhere; only a live one belongs to the app.
      [ ! -e "$dest" ] || die "acp is managed by acp.app; update it from the app (or remove the link to install the CLI separately)"
      say "replacing the link to acp.app, which is gone ($old)"
      ;;
    *) die "$dest is a link to $old, not acp; move it away and run again" ;;
  esac
elif [ -e "$dest" ]; then
  LC_ALL=C grep -aq "$marker" "$dest" || die "$dest exists and is not acp; move it away and run again"
fi
cp "$new" "$dir/.acp.$$"
chmod 755 "$dir/.acp.$$"
mv -f "$dir/.acp.$$" "$dest"
say "installed $("$dest" --version) at $dest"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "$dir is not on your PATH; add it in your shell profile, e.g. export PATH=\"$dir:\$PATH\"" ;;
esac

if [ "${ACP_NO_INIT:-}" != 1 ]; then
  "$dest" init
fi
