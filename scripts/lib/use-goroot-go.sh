# Sourced by the build and verify scripts:
#   . "$root/scripts/lib/use-goroot-go.sh"
#   use_goroot_go build-desktop
#
# A GOROOT exported by a version manager (mise, asdf, ...) while another go is first on PATH makes
# every build fail with "compile: version X does not match go tool version Y". When the go on PATH
# and the go that owns GOROOT differ, use_goroot_go puts the GOROOT one first on PATH for the rest
# of the script, saying so on stderr under the given label. Without go on PATH it does nothing: the
# script's own go command reports that.
use_goroot_go() {
  command -v go >/dev/null 2>&1 || return 0
  _goroot=$(go env GOROOT)
  if [ -x "$_goroot/bin/go" ] && [ "$("$_goroot/bin/go" env GOVERSION)" != "$(go env GOVERSION)" ]; then
    echo "$1: $(command -v go) is $(go env GOVERSION) but GOROOT is $_goroot; using $_goroot/bin/go" >&2
    PATH=$_goroot/bin:$PATH
    export PATH
  fi
  unset _goroot
}
