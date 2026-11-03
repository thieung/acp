#!/bin/sh
# Verify acp on this machine: every check the GitHub workflows ci.yml and desktop.yml used to run.
#   sh scripts/verify.sh                  # the working tree: tracked and untracked, not ignored
#   sh scripts/verify.sh --rev HEAD       # one commit, exported clean (what the pre-push hook does)
#   --skip-linux    no Docker run (go vet, go test -race and the installer tests on Linux)
#   --skip-desktop  no macOS app (swift test, build-desktop.sh, desktop-smoke.sh, which opens the app)
# It runs in a throwaway copy, so the checkout and its dist/ are never written. Each step logs to a
# file; the first failure prints the end of its log, keeps the copy for a look and exits 1.
set -eu

# Git runs hooks with GIT_DIR and its siblings pointing at the repo being pushed. Left set, every
# git command the tests run in their throwaway fixtures acts on this repo instead: a pre-push run
# once set core.bare, added a branch and a worktree and moved HEAD. Drop them all; git finds the
# repo from $root by itself.
unset $(git rev-parse --local-env-vars 2>/dev/null) 2>/dev/null || true

usage() { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//' >&2; exit "${1:-2}"; }
rev=
linux=1
desktop=1
while [ $# -gt 0 ]; do
  case $1 in
    --rev) [ $# -ge 2 ] || usage; rev=$2; shift 2 ;;
    --skip-linux) linux=; shift ;;
    --skip-desktop) desktop=; shift ;;
    -h|--help) usage 0 ;;
    *) echo "verify: unknown argument $1" >&2; usage ;;
  esac
done

root=$(cd "$(dirname "$0")/.." && pwd)
tag=v0.0.0-verify
fail() { echo "verify: $*" >&2; exit 1; }
[ "$(uname -s)" = Darwin ] || desktop=

# A GOROOT exported by a version manager while another go is first on PATH makes every build fail
# with "compile: version X does not match go tool version Y". Use the go that owns that GOROOT.
command -v go >/dev/null 2>&1 || fail "go is not on PATH"
goroot=$(go env GOROOT)
if [ -x "$goroot/bin/go" ] && [ "$("$goroot/bin/go" env GOVERSION)" != "$(go env GOVERSION)" ]; then
  echo "verify: $(command -v go) is $(go env GOVERSION) but GOROOT is $goroot; using $goroot/bin/go" >&2
  PATH=$goroot/bin:$PATH
  export PATH
fi
if [ -n "$linux" ]; then
  docker info >/dev/null 2>&1 || fail "Docker is not running: start it (OrbStack, Docker Desktop) or pass --skip-linux"
fi
if [ -n "$desktop" ]; then
  command -v swift >/dev/null 2>&1 || fail "swift is missing: install the Xcode Command Line Tools or pass --skip-desktop"
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/acp-verify.XXXXXX")
src=$work/src
logs=$work/logs
mkdir -p "$src" "$logs"
ok=
cleanup() {
  if [ -n "$ok" ]; then rm -rf "$work"; else echo "verify: kept $work" >&2; fi
}
trap cleanup EXIT

if [ -n "$rev" ]; then
  sha=$(git -C "$root" rev-parse --verify --quiet "$rev^{commit}") || fail "$rev is not a commit"
  git -C "$root" archive "$sha" | tar -x -C "$src"
  echo "verify: commit $sha"
else
  # Deleted files are still in the index; leave them out.
  (cd "$root" && git -c core.quotePath=false ls-files --cached --others --exclude-standard |
    while IFS= read -r f; do if [ -e "$f" ] || [ -L "$f" ]; then printf '%s\n' "$f"; fi; done |
    tar -cf - -T -) | tar -x -C "$src"
  echo "verify: working tree of $root"
fi

n=0
step() { # name command...
  name=$1
  shift
  n=$((n + 1))
  log=$logs/$n.log
  printf '%-36s ' "$name"
  start=$(date +%s)
  if (cd "$src" && "$@") </dev/null >"$log" 2>&1; then
    echo "ok $(($(date +%s) - start))s"
  else
    echo "FAIL $(($(date +%s) - start))s"
    tail -n 60 "$log" >&2
    fail "$name failed; full log: $log"
  fi
}

# ci.yml, test job (this OS) and install job.
step "go vet" go vet ./...
step "go test -race" go test -race ./...
step "build-release.sh" sh scripts/build-release.sh "$tag"
step "test-install.sh (sh)" sh scripts/tests/test-install.sh
step "test-install.sh (bash)" env TEST_SHELL=bash sh scripts/tests/test-install.sh
step "install-smoke.sh" sh scripts/tests/install-smoke.sh

# ci.yml on ubuntu-latest: the same checks in a Go container of the go.mod version, as a non-root
# user like the GitHub runner. Module and build caches live in the Docker volume acp-verify-go.
# --init puts a reaper at PID 1, as a real machine has: the tests that kill a process group check
# with kill(pid, 0), which still succeeds on a killed grandchild left as a zombie.
linux_checks() {
  image=golang:$(sed -n 's/^go \([0-9][0-9.]*\).*/\1/p' go.mod)
  docker run --rm -i --init -v "$src:/src:ro" -v acp-verify-go:/cache "$image" sh -s "$tag" <<'EOF'
set -eu
id verify >/dev/null 2>&1 || useradd -m verify
chown verify /cache
exec su verify -s /bin/sh -c '
  set -eux
  export PATH=/usr/local/go/bin:$PATH GOCACHE=/cache/build GOMODCACHE=/cache/mod
  cp -R /src "$HOME/src"
  cd "$HOME/src"
  go version
  go vet ./...
  go test -race ./...
  sh scripts/build-release.sh "$1"
  sh scripts/tests/test-install.sh
  TEST_SHELL=bash sh scripts/tests/test-install.sh
  sh scripts/tests/install-smoke.sh
' verify "$1"
EOF
}
if [ -n "$linux" ]; then
  step "linux: vet, test -race, installer" linux_checks
fi

# desktop.yml. The copy builds from a fresh desktop/.build: Swift's module cache and
# workspace-state.json hold absolute paths, so the checkout's .build cannot be reused elsewhere.
# SwiftPM keeps fetched packages in its shared cache (~/Library/Caches/org.swift.swiftpm).
if [ -n "$desktop" ]; then
  step "swift test" sh -c 'cd desktop && swift test'
  step "build-desktop.sh" sh scripts/build-desktop.sh "$tag"
  step "desktop-smoke.sh" sh scripts/tests/desktop-smoke.sh dist/desktop/acp.app "$tag"
fi

ok=1
echo "verify: all checks passed"
