#!/bin/sh
# Print the app's CFBundleVersion for a release tag. Sparkle orders updates by CFBundleVersion and
# its comparator ignores everything from a "-" on (0.1.0-beta.1 = 0.1.0-beta.2 = 0.1.0), so the
# prerelease becomes a fourth number: alpha.N is 1000+N, beta.N 2000+N, rc.N 3000+N, any other
# lowercase label (dev, ci) N, and the final release 9000, above all of its prereleases. The
# semver itself stays in CFBundleShortVersionString.
#   scripts/bundle-version.sh v0.1.0-beta.2   # 0.1.0.2002
set -eu

tag=${1:?usage: scripts/bundle-version.sh TAG}
version=${tag#v}
bad() { echo "bundle-version: $tag is not vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-LABEL[.N] (N < 1000)" >&2; exit 2; }

core=${version%%-*}
case $core in ''|*[!0-9.]*|.*|*.|*..*) bad ;; esac
[ "$(printf %s "$core" | tr -cd . | wc -c | tr -d ' ')" = 2 ] || bad
if [ "$core" = "$version" ]; then
  echo "$core.9000"
  exit 0
fi

pre=${version#*-}
label=${pre%%[.0-9]*}
n=${pre#"$label"}
n=${n#.}
case $label in ''|*[!abcdefghijklmnopqrstuvwxyz]*) bad ;; esac  # a-z would match capitals in some locales
case $n in *[!0-9]*) bad ;; esac
n=$(printf %s "$n" | sed 's/^0*//')
[ ${#n} -le 3 ] || bad
case $label in alpha) rank=1 ;; beta) rank=2 ;; rc) rank=3 ;; *) rank=0 ;; esac
echo "$core.$((rank * 1000 + ${n:-0}))"
