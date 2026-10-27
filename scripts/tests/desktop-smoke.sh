#!/bin/sh
# Check a built acp.app: universal binaries, the Frameworks rpath, a valid signature, the bundled
# acp's version, the Go contract the shell relies on (init, web --print-url, the dashboard and its
# session), then launch the real app in smoke mode.
#   sh scripts/build-desktop.sh v0.0.0-dev && sh scripts/tests/desktop-smoke.sh dist/desktop/acp.app v0.0.0-dev
set -eu

app=${1:?usage: scripts/tests/desktop-smoke.sh APP TAG}
tag=${2:?usage: scripts/tests/desktop-smoke.sh APP TAG}
app=$(cd "$app" && pwd)
shell_bin=$app/Contents/MacOS/AcpDesktop
acp=$app/Contents/Helpers/acp
work=$(mktemp -d)
web_pid=
cleanup() {
  [ -z "$web_pid" ] || kill "$web_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

for bin in "$shell_bin" "$acp"; do
  [ -x "$bin" ] || fail "missing $bin"
  archs=$(lipo -archs "$bin")
  case " $archs " in *" x86_64 "*) ;; *) fail "$bin lacks x86_64 (has: $archs)" ;; esac
  case " $archs " in *" arm64 "*) ;; *) fail "$bin lacks arm64 (has: $archs)" ;; esac
done
[ -d "$app/Contents/Frameworks/Sparkle.framework" ] || fail "Sparkle.framework is not bundled"
otool -l "$shell_bin" | grep -q '@executable_path/../Frameworks' || fail "AcpDesktop has no Frameworks rpath"
codesign --verify --deep --strict "$app" || fail "codesign --verify failed"
[ "$("$acp" --version)" = "acp $tag" ] || fail "bundled acp is $("$acp" --version), want acp $tag"

# Sparkle orders updates by CFBundleVersion, and its comparator ignores a "-beta.1" suffix, so the
# bundle carries the numeric form scripts/bundle-version.sh makes. Ask the bundled Sparkle itself
# that every step of a release ladder compares newer than the one before.
root=$(cd "$(dirname "$0")/../.." && pwd)
want=$(sh "$root/scripts/bundle-version.sh" "$tag")
got=$(plutil -extract CFBundleVersion raw "$app/Contents/Info.plist")
[ "$got" = "$want" ] || fail "CFBundleVersion is $got, want $want"
cat >"$work/compare.m" <<'OBJC'
#import <Foundation/Foundation.h>
#import <Sparkle/SUStandardVersionComparator.h>
int main(int argc, const char **argv) {
  @autoreleasepool {
    NSComparisonResult r = [[SUStandardVersionComparator defaultComparator] compareVersion:@(argv[1])
                                                                                toVersion:@(argv[2])];
    puts(r == NSOrderedAscending ? "<" : r == NSOrderedSame ? "=" : ">");
  }
  return 0;
}
OBJC
frameworks=$app/Contents/Frameworks
clang -fobjc-arc -F "$frameworks" -framework Foundation -framework Sparkle -Wl,-rpath,"$frameworks" \
  "$work/compare.m" -o "$work/compare" || fail "cannot build the Sparkle version check"
prev=
for t in v0.0.0-dev v0.1.0-alpha.1 v0.1.0-beta.1 v0.1.0-beta.2 v0.1.0-beta.9 v0.1.0-beta.10 v0.1.0-rc.1 \
  v0.1.0 v0.1.1-beta.1 v0.1.1 v0.2.0 v1.0.0-rc.2 v1.0.0; do
  v=$(sh "$root/scripts/bundle-version.sh" "$t") || fail "bundle-version.sh rejected $t"
  if [ -n "$prev" ] && [ "$("$work/compare" "$prev" "$v")" != "<" ]; then
    fail "Sparkle does not see $t ($v) as newer than $prev"
  fi
  prev=$v
done
for t in v1.2 v1.2.3-beta.1000 v1.2.3-Beta.1 v1.2.3+build v1.2.3-beta.1.2; do
  if sh "$root/scripts/bundle-version.sh" "$t" >/dev/null 2>&1; then fail "bundle-version.sh accepted $t"; fi
done

# The Go side, as the app runs it, in a throwaway home: nothing of this machine is read or written.
mkdir -p "$work/home" "$work/xdg"
run_acp() {
  env -i HOME="$work/home" XDG_CONFIG_HOME="$work/xdg" ACP_ROOT="$work/home/agent-control-plane" \
    PATH=/usr/bin:/bin "$acp" "$@"
}
run_acp init --json >"$work/init.json" || fail "acp init: $(cat "$work/init.json")"
grep -q '"kind": "acp.init"' "$work/init.json" || fail "acp init printed no acp.init envelope"

# A fifo keeps acp web's stdin open until this script lets go of it (--exit-with-parent).
mkfifo "$work/stdin"
run_acp web --port 0 --no-open --print-url --exit-with-parent <"$work/stdin" >"$work/web.out" 2>"$work/web.err" &
web_pid=$!
exec 3>"$work/stdin"
url=
i=0
while [ $i -lt 100 ]; do
  url=$(sed -n 's/^sign in: //p' "$work/web.out")
  [ -n "$url" ] && break
  kill -0 "$web_pid" 2>/dev/null || fail "acp web exited: $(cat "$work/web.err")"
  sleep 0.1
  i=$((i + 1))
done
[ -n "$url" ] || fail "acp web printed no sign-in line"
base=${url%%/#code=*}
code=${url##*#code=}
curl -fsS -D "$work/headers" -o /dev/null "$base/" || fail "GET / failed"
grep -qi '^content-security-policy:' "$work/headers" || fail "GET / has no Content-Security-Policy"
status=$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' \
  --data "{\"code\":\"$code\"}" "$base/api/session")
[ "$status" = 200 ] || fail "POST /api/session returned $status"

# Closing stdin is how the app stops acp web.
exec 3>&-
i=0
while kill -0 "$web_pid" 2>/dev/null; do
  [ $i -lt 100 ] || fail "acp web did not exit when stdin closed"
  sleep 0.1
  i=$((i + 1))
done
wait "$web_pid" || fail "acp web exited with an error: $(cat "$work/web.err")"
web_pid=

# The real app, in smoke mode: it loads the dashboard once and exits 0. The app hands acp the
# login shell's PATH and ACP_ROOT, so HOME and SHELL point into the throwaway home too.
printf 'export ACP_ROOT=%s\n' "$work/home/agent-control-plane" >"$work/home/.profile"
env -i HOME="$work/home" USER="${USER:-smoke}" TMPDIR="$work" SHELL=/bin/sh PATH=/usr/bin:/bin \
  ACP_DESKTOP_SMOKE=1 "$shell_bin" >"$work/app.out" 2>"$work/app.err" &
app_pid=$!
i=0
while kill -0 "$app_pid" 2>/dev/null; do
  if [ $i -ge 600 ]; then
    kill "$app_pid" 2>/dev/null || true
    fail "the app did not exit within 60 s: $(cat "$work/app.out" "$work/app.err")"
  fi
  sleep 0.1
  i=$((i + 1))
done
rc=0
wait "$app_pid" || rc=$?
if grep -q 'Library not loaded' "$work/app.err"; then fail "the app cannot load a library: $(cat "$work/app.err")"; fi
[ "$rc" = 0 ] || fail "the app exited $rc: $(cat "$work/app.out" "$work/app.err")"
grep -q '^smoke: loaded http://127.0.0.1:[0-9]*/' "$work/app.out" || fail "no 'smoke: loaded' line: $(cat "$work/app.out")"
if grep -q 'code=' "$work/app.out"; then fail "the smoke line carries the one-time code"; fi
echo "desktop smoke: ok"
