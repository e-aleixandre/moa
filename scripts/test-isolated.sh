#!/bin/bash
# Run a command (e.g. a freshly built moa binary) with a private HOME and
# config directory under /tmp, keeping the Go caches shared.
#
#   scripts/test-isolated.sh ./moa --version
set -eu

if [ $# -eq 0 ]; then
	echo "usage: $0 command [args...]" >&2
	exit 2
fi

# Resolve the Go locations while HOME is still the real one: Go derives its
# defaults from HOME, so they would otherwise point into the sandbox and force
# a full re-download and rebuild.
GOPATH="${GOPATH:-$(go env GOPATH)}"
GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export GOPATH GOMODCACHE GOCACHE

root=$(mktemp -d /tmp/moa-isolated.XXXXXX)
cleanup() {
	chmod -R u+w "$root" 2>/dev/null || true
	rm -rf "$root"
}
trap cleanup EXIT

mkdir "$root/home" "$root/config" "$root/xdg" "$root/tmp"
export HOME="$root/home" MOA_CONFIG_DIR="$root/config" XDG_CONFIG_HOME="$root/xdg" TMPDIR="$root/tmp"

# Bash restores SIGINT in the asynchronous subshell before exec. The parent
# stays alive to forward signals and remove the sandbox after the command ends.
(trap - INT; exec "$@") <&0 &
child=$!
forward() {
	kill -"$1" "$child" 2>/dev/null || true
	wait "$child" 2>/dev/null || true
	exit "$2"
}
trap 'forward TERM 143' TERM
trap 'forward INT 130' INT
rc=0
wait "$child" || rc=$?
exit "$rc"
