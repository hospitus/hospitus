#!/bin/sh
#
# hospitus-macos-service.sh — run hospitusd as a launchd agent on macOS.
#
# macOS has no rc.d, so this is the counterpart of etc/rc.d/hospitus: it creates
# the directories, generates an API key on first install, renders the plist
# template with the resolved paths, and loads the agent.
#
# The agent runs as the logged-in user, not as root. The providers available on
# macOS need no privileges: QEMU accelerates through Hypervisor.framework and
# Podman talks to a user-owned podman machine. Everything it writes — data
# directory, API key, VM disks — stays owned by that user.
#
# Every subcommand is idempotent: install on an already-installed host
# reinstalls in place, uninstall on a clean host succeeds and changes nothing.
#
# Usage:
#   sh scripts/hospitus-macos-service.sh install [-a addr] [-d data-dir] [-b bin-dir]
#   sh scripts/hospitus-macos-service.sh uninstall [-p]
#   sh scripts/hospitus-macos-service.sh status
#   sh scripts/hospitus-macos-service.sh key
#
#   -a  listen address (default: 127.0.0.1:8080)
#   -d  data directory (default: ~/Library/Application Support/hospitus)
#   -b  directory the binaries are installed into (default: ~/.local/bin)
#   -p  uninstall only: also remove the data directory and the API key

set -eu

LABEL="io.github.hospitus.hospitus"
ADDR="127.0.0.1:8080"
DATA_DIR="$HOME/Library/Application Support/hospitus"
LOG_DIR="$HOME/Library/Logs/hospitus"
BIN_DIR="$HOME/.local/bin"
AGENT_DIR="$HOME/Library/LaunchAgents"
PLIST="$AGENT_DIR/$LABEL.plist"
PURGE=0

step() { printf '==> %s\n' "$1"; }
info() { printf '    %s\n' "$1"; }
die() {
	printf '%s\n' "$1" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: sh hospitus-macos-service.sh <install|uninstall|status|key> [options]

  install    render the launchd agent, generate the API key, load it
  uninstall  unload and remove the agent (-p also removes data and key)
  status     show whether the agent is loaded and the daemon answering
  key        print the API key

  -a addr    listen address (default 127.0.0.1:8080)
  -d dir     data directory (default ~/Library/Application Support/hospitus)
  -b dir     install the binaries here (default ~/.local/bin)
  -p         uninstall only: also remove data directory and API key
EOF
	exit "${1:-0}"
}

[ $# -ge 1 ] || usage 1
ACTION=$1
shift

while getopts "a:d:b:ph" opt; do
	case "$opt" in
	a) ADDR="$OPTARG" ;;
	d) DATA_DIR="$OPTARG" ;;
	b) BIN_DIR="$OPTARG" ;;
	p) PURGE=1 ;;
	h) usage 0 ;;
	*) usage 1 ;;
	esac
done

[ "$(uname -s)" = "Darwin" ] || die "this script is for macOS; on FreeBSD use the rc.d service"
[ "$(id -u)" -ne 0 ] || die "run this as your own user, not root: the agent is a LaunchAgent"

STATE_DIR="$DATA_DIR/state"
DB="$DATA_DIR/hospitus.db"
KEY_FILE="$DATA_DIR/api.key"

# repo_root locates the checkout this script lives in, so the plist template and
# the binaries can be found without the caller having to cd anywhere.
repo_root() {
	CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd
}

# install_binaries copies the built binaries into BIN_DIR and echoes the daemon
# path the agent should run.
#
# The agent must not point at the checkout. launchd refuses to execute a binary
# on a volume it has no access to — a build on an external disk hangs in dyld
# with an empty log and no error — and rebuilding the repository would otherwise
# swap the running daemon's binary underneath it.
install_binaries() {
	root=$(repo_root)
	[ -x "$root/hospitusd" ] || die "no hospitusd in $root: run 'make build' first"

	mkdir -p "$BIN_DIR"
	cp "$root/hospitusd" "$BIN_DIR/hospitusd"
	chmod 0755 "$BIN_DIR/hospitusd"
	if [ -x "$root/hospitus" ]; then
		cp "$root/hospitus" "$BIN_DIR/hospitus"
		chmod 0755 "$BIN_DIR/hospitus"
	fi

	printf '%s\n' "$BIN_DIR/hospitusd"
}

generate_key() {
	[ -f "$KEY_FILE" ] && return 0

	# 32 random bytes, hex-encoded, using only base-system tools. Capture into a
	# variable first: a piped redirection reports the status of the last stage
	# only, so a failure would still leave an empty file behind.
	_key=$(head -c 32 /dev/urandom | xxd -p | tr -d '\n')
	[ -n "$_key" ] || die "failed to generate an API key"

	(
		umask 077
		printf '%s\n' "$_key" >"$KEY_FILE"
	)
	chmod 0600 "$KEY_FILE"
	info "generated API key at $KEY_FILE"
}

agent_loaded() {
	launchctl list 2>/dev/null | grep -q "$LABEL"
}

unload_agent() {
	agent_loaded || return 0
	# bootout is the modern spelling; fall back for older releases.
	launchctl bootout "gui/$(id -u)/$LABEL" >/dev/null 2>&1 ||
		launchctl unload "$PLIST" >/dev/null 2>&1 || true
}

case "$ACTION" in
install)
	template="$(repo_root)/etc/launchd/$LABEL.plist.in"
	[ -f "$template" ] || die "plist template not found at $template"

	step "Creating directories"
	mkdir -p "$DATA_DIR" "$STATE_DIR" "$LOG_DIR" "$AGENT_DIR"

	step "Installing binaries into $BIN_DIR"
	hospitusd=$(install_binaries)
	info "$hospitusd"

	step "Generating the API key"
	generate_key

	step "Rendering $PLIST"
	# The paths may contain spaces (~/Library/Application Support), so every
	# substitution goes through a shell variable rather than the sed script.
	# A path holding "|", "&" or a backslash would otherwise end the substitution
	# or be re-read by sed as part of the replacement.
	sed_escape() { printf '%s' "$1" | sed -e 's/[\\|&]/\\&/g'; }

	sed \
		-e "s|@HOSPITUSD@|$(sed_escape "$hospitusd")|g" \
		-e "s|@ADDR@|$(sed_escape "$ADDR")|g" \
		-e "s|@DATA_DIR@|$(sed_escape "$DATA_DIR")|g" \
		-e "s|@STATE_DIR@|$(sed_escape "$STATE_DIR")|g" \
		-e "s|@DB@|$(sed_escape "$DB")|g" \
		-e "s|@KEY_FILE@|$(sed_escape "$KEY_FILE")|g" \
		-e "s|@LOG_DIR@|$(sed_escape "$LOG_DIR")|g" \
		"$template" >"$PLIST"

	plutil -lint "$PLIST" >/dev/null || die "rendered plist is not valid: $PLIST"

	step "Loading the agent"
	unload_agent
	launchctl bootstrap "gui/$(id -u)" "$PLIST" >/dev/null 2>&1 ||
		launchctl load "$PLIST" >/dev/null 2>&1 ||
		die "launchctl refused to load $PLIST"

	step "Done"
	echo
	echo "hospitusd is listening on $ADDR. Point the CLI at it with:"
	echo "  hospitus context add local --url http://$ADDR --api-key \"\$(cat '$KEY_FILE')\""
	;;

uninstall)
	step "Unloading the agent"
	unload_agent
	rm -f "$PLIST"

	step "Removing installed binaries"
	rm -f "$BIN_DIR/hospitusd" "$BIN_DIR/hospitus"

	if [ "$PURGE" -eq 1 ]; then
		step "Removing data directory and logs"
		rm -rf "$DATA_DIR" "$LOG_DIR"
	else
		step "Keeping $DATA_DIR (pass -p to remove it)"
	fi

	step "Done"
	;;

status)
	if agent_loaded; then
		echo "agent:  loaded ($LABEL)"
	else
		echo "agent:  not loaded"
	fi
	if curl -fsS -m 3 "http://$ADDR/health" >/dev/null 2>&1; then
		echo "daemon: answering on $ADDR"
	else
		echo "daemon: not answering on $ADDR"
	fi
	echo "binary: $BIN_DIR/hospitusd"
	echo "data:   $DATA_DIR"
	echo "logs:   $LOG_DIR"
	;;

key)
	[ -f "$KEY_FILE" ] || die "no API key at $KEY_FILE — run 'install' first"
	cat "$KEY_FILE"
	;;

*)
	usage 1
	;;
esac
