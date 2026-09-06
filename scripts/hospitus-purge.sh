#!/bin/sh
#
# hospitus-purge.sh — remove every trace of Hospitus from a FreeBSD host.
#
# The script is idempotent: on a host where Hospitus was never installed, or on one
# it has already cleaned, it succeeds and changes nothing. That makes it usable
# both as an uninstaller and as a reset step before reinstalling from scratch.
#
# What it removes, in order:
#   1. running instances created by Hospitus (jails under the parent dataset,
#      bhyve VMs recorded in the data directory)
#   2. the daemon and its rc.conf entry
#   3. the rules Hospitus loaded into its own "hospitus" PF anchor
#   4. binaries, rc.d script and configuration
#   5. the data directory: database, API key, master key, instance state
#   6. the ZFS datasets under the Hospitus parent dataset
#
# What it never touches:
#   - /etc/pf.conf. Hospitus manages only its own anchor; removing the anchor
#     declaration from pf.conf is the operator's decision, and the script
#     prints a reminder instead of editing the file.
#   - any ZFS dataset outside the parent dataset.
#
# Usage:
#   doas sh scripts/hospitus-purge.sh [-y] [-k] [-p parent-dataset]
#
#   -y   assume yes; do not prompt before destroying data
#   -k   keep data: skip steps 5 and 6 (files only, like `make uninstall`)
#   -p   ZFS parent dataset to destroy
#        (default: $HOSPITUS_ZFS_PARENT, else zroot/hospitus)

set -eu

ASSUME_YES=0
KEEP_DATA=0
PARENT="${HOSPITUS_ZFS_PARENT:-zroot/hospitus}"
BRIDGE="${HOSPITUS_BRIDGE:-hospitus0}"
DATA_DIR="${HOSPITUS_DATA_DIR:-/var/lib/hospitus}"
# Where packages live. LOCALBASE is a ports make variable, not an exported
# environment one, so it is unset in a plain shell and under rc(8); the sysctl
# is what a running system answers with.
LOCALBASE="${LOCALBASE:-$(sysctl -n user.localbase 2>/dev/null || echo /usr/local)}"
CONF_DIR="$LOCALBASE/etc/hospitus"
LOG_DIR="/var/log/hospitus"
BIN_DIR="$LOCALBASE/bin"
# Service names this or any earlier install may have left in rc.d. "hospitusd" is
# what installs before 1.0.0 used.
RC_SERVICES="hospitus hospitusd"

# This script destroys recursively. Everything it is pointed at comes from a
# flag or the environment, so check the targets are ours before touching them:
# "-p zroot" would take the operating system with it.
guard_targets() {
	case "$PARENT" in
	*/*) ;;
	*)
		echo "refusing to destroy $PARENT: that is a whole pool, not a Hospitus dataset" >&2
		exit 1
		;;
	esac
	case "$DATA_DIR" in
	/*/*) ;;
	*)
		echo "refusing to remove $DATA_DIR: expected a path at least two levels deep" >&2
		exit 1
		;;
	esac
	case "$LOG_DIR" in
	/*/*) ;;
	*)
		echo "refusing to remove $LOG_DIR: expected a path at least two levels deep" >&2
		exit 1
		;;
	esac
}

usage() {
	cat <<'EOF'
Usage: doas sh hospitus-purge.sh [-y] [-k] [-p parent-dataset]

  -y   assume yes; do not prompt before destroying data
  -k   keep data: remove files only, leave the data directory and ZFS datasets
  -p   ZFS parent dataset to destroy (default: $HOSPITUS_ZFS_PARENT, else zroot/hospitus)
  -b   bridge Hospitus created for instance networking (default: $HOSPITUS_BRIDGE, else hospitus0)
EOF
	exit "${1:-0}"
}

while getopts "ykp:b:h" opt; do
	case "$opt" in
	y) ASSUME_YES=1 ;;
	k) KEEP_DATA=1 ;;
	p) PARENT="$OPTARG" ;;
	b) BRIDGE="$OPTARG" ;;
	h) usage 0 ;;
	*) usage 1 ;;
	esac
done

step() { printf '==> %s\n' "$1"; }
info() { printf '    %s\n' "$1"; }

if [ "$(id -u)" -ne 0 ]; then
	echo "hospitus-purge must run as root (doas sh $0 ...)" >&2
	exit 1
fi

guard_targets

# Strip a trailing slash so "zroot/hospitus/" and "zroot/hospitus" behave the same.
PARENT=$(printf '%s' "$PARENT" | sed 's|/*$||')
if [ -z "$PARENT" ]; then
	echo "parent dataset cannot be empty" >&2
	exit 1
fi
case "$PARENT" in
*/*) ;;
*)
	# Refuse a bare pool name: destroying "zroot" would take the whole system.
	printf 'refusing to operate on pool root %s: pass a child dataset, e.g. -p zroot/hospitus\n' "$PARENT" >&2
	exit 1
	;;
esac

if [ "$KEEP_DATA" -eq 0 ] && [ "$ASSUME_YES" -eq 0 ]; then
	echo "This will destroy:"
	echo "  - the data directory $DATA_DIR (database, API key, master key)"
	echo "  - the ZFS dataset $PARENT and everything under it"
	echo "  - every jail and bhyve VM Hospitus created"
	printf 'Continue? [y/N] '
	read -r answer
	case "$answer" in
	y | Y | yes | YES) ;;
	*)
		echo "aborted"
		exit 1
		;;
	esac
fi

# ---------------------------------------------------------------- instances --

# stop_jails_under stops every running jail whose root is inside the given
# directory, so their datasets can be destroyed. Jails outside it are left alone.
stop_jails_under() {
	root=$1
	[ -d "$root" ] || return 0
	command -v jls >/dev/null 2>&1 || return 0

	jls -h jid path 2>/dev/null | while read -r jid path; do
		[ "$jid" = "jid" ] && continue
		case "$path" in
		"$root"/*)
			info "stopping jail $jid ($path)"
			jail -r "$jid" >/dev/null 2>&1 || true
			;;
		esac
	done
}

# unmount_under unmounts every filesystem mounted below the given directory,
# deepest first. Stopping a jail leaves its devfs behind, and any lingering
# mount makes "zfs destroy" fail with "dataset is busy".
unmount_under() {
	root=$1
	[ -d "$root" ] || return 0

	mount -p | awk -v root="$root" '$2 != root && index($2, root "/") == 1 { print $2 }' |
		sort -r |
		while read -r mp; do
			info "unmounting $mp"
			umount -f "$mp" >/dev/null 2>&1 || true
		done
}

# destroy_bhyve_vms destroys the bhyve VMs Hospitus recorded in its data directory.
# Only those names are touched: VMs another tool owns must survive.
destroy_bhyve_vms() {
	[ -d "$DATA_DIR/bhyve" ] || return 0
	command -v bhyvectl >/dev/null 2>&1 || return 0

	for vmdir in "$DATA_DIR"/bhyve/*; do
		[ -d "$vmdir" ] || continue
		vm=$(basename "$vmdir")
		[ -e "/dev/vmm/$vm" ] || continue
		info "destroying bhyve VM $vm"
		bhyvectl --destroy --vm="$vm" >/dev/null 2>&1 || true
	done
}

if [ "$KEEP_DATA" -eq 0 ]; then
	step "Stopping instances created by Hospitus"
	mountpoint=""
	if command -v zfs >/dev/null 2>&1; then
		mountpoint=$(zfs get -H -o value mountpoint "$PARENT" 2>/dev/null || true)
	fi
	case "$mountpoint" in
	/*) stop_jails_under "$mountpoint" ;;
	esac
	stop_jails_under "$DATA_DIR"
	destroy_bhyve_vms

	# Release the mounts those instances left behind, or the datasets below
	# cannot be destroyed.
	case "$mountpoint" in
	/*) unmount_under "$mountpoint" ;;
	esac
	unmount_under "$DATA_DIR"
fi

# destroy_hospitus_bridge removes the bridge Hospitus creates for instance networking
# along with the epair interfaces still attached to it. Only members of that
# bridge are touched, so interfaces belonging to something else survive.
destroy_hospitus_bridge() {
	bridge=$1
	command -v ifconfig >/dev/null 2>&1 || return 0
	ifconfig "$bridge" >/dev/null 2>&1 || return 0

	# Destroying the host side of an epair takes its peer with it.
	for member in $(ifconfig "$bridge" 2>/dev/null | awk '$1 == "member:" { print $2 }'); do
		case "$member" in
		epair*)
			info "destroying $member"
			ifconfig "$member" destroy >/dev/null 2>&1 || true
			;;
		esac
	done

	info "destroying bridge $bridge"
	ifconfig "$bridge" destroy >/dev/null 2>&1 || true
}

# ------------------------------------------------------------------ service --

step "Stopping and disabling the daemon"
# Both the current service name and the one earlier installs used. An rc.d
# script left behind under the old name survives a purge that only knows the
# current one, and then wins the race at the next boot: it starts the daemon
# with its own arguments — no --api-key-file among them — and the real service
# is refused because the data directory is already locked. Nothing shows until
# the machine reboots.
for svc in $RC_SERVICES; do
	if [ -x "$LOCALBASE/etc/rc.d/$svc" ]; then
		service "$svc" stop >/dev/null 2>&1 || true
	fi
	sysrc -x "${svc}_enable" >/dev/null 2>&1 || true
done
# Every rcvar the service reads, not just _enable. A leftover hospitus_flags names
# the TLS certificate this script is about to delete, so a reinstall that does
# not recreate it starts a daemon that refuses to serve.
for var in $(sysrc -a 2>/dev/null | sed -n 's/^\(hospitus[a-z0-9_]*\):.*/\1/p'); do
	sysrc -x "$var" >/dev/null 2>&1 || true
done
# A daemon started by hand has no rc entry; stop it too so files are not held.
pkill -x hospitusd >/dev/null 2>&1 || true

# -------------------------------------------------------------------- network --

if [ "$KEEP_DATA" -eq 0 ]; then
	step "Removing the Hospitus bridge and its epair interfaces"
	destroy_hospitus_bridge "$BRIDGE"
fi

# ----------------------------------------------------------------------- pf --

step "Flushing the hospitus PF anchor"
if command -v pfctl >/dev/null 2>&1 && pfctl -s info >/dev/null 2>&1; then
	# -a hospitus scopes every flush to the anchor Hospitus owns; the host ruleset in
	# /etc/pf.conf is never read or written here.
	pfctl -a hospitus -F all >/dev/null 2>&1 || true
	info "anchor flushed (pf.conf left untouched)"
else
	info "PF not enabled — nothing to flush"
fi

# -------------------------------------------------------------------- files --

step "Removing binaries, service script and configuration"
rm -f "$BIN_DIR/hospitus" "$BIN_DIR/hospitusd"
for svc in $RC_SERVICES; do
	rm -f "$LOCALBASE/etc/rc.d/$svc"
done
# A file under CONF_DIR that pf.conf includes cannot simply be deleted: pf
# refuses a ruleset whose include names a file that is not there, so the next
# "service pf reload" — or the next boot — would fail on the whole ruleset and
# leave the host with whatever was loaded before. pf.conf is the operator's and
# this script never edits it, so the file stays and the operator is told.
pf_includes_from_conf_dir() {
	[ -r /etc/pf.conf ] || return 1
	grep -qE "^[[:space:]]*include[[:space:]]+\"?$CONF_DIR/" /etc/pf.conf
}

if pf_includes_from_conf_dir; then
	included=$(grep -oE "$CONF_DIR/[^\"]*" /etc/pf.conf | head -1)
	info "keeping $included: /etc/pf.conf includes it"
	find "$CONF_DIR" -mindepth 1 ! -path "$included" -delete 2>/dev/null || true
	PF_INCLUDE_KEPT="$included"
else
	rm -rf "$CONF_DIR"
fi

# --------------------------------------------------------------------- data --

if [ "$KEEP_DATA" -eq 1 ]; then
	step "Keeping data ($DATA_DIR and $PARENT)"
else
	step "Removing the data directory"
	rm -rf "$DATA_DIR"
	rm -rf "$LOG_DIR"

	step "Destroying ZFS datasets under $PARENT"
	if command -v zfs >/dev/null 2>&1 && zfs list "$PARENT" >/dev/null 2>&1; then
		if zfs destroy -r "$PARENT"; then
			info "destroyed $PARENT"
		else
			echo "failed to destroy $PARENT — an instance may still be running" >&2
			echo "check with: zfs list -r $PARENT ; jls ; ls /dev/vmm" >&2
			exit 1
		fi
	else
		info "$PARENT does not exist — nothing to destroy"
	fi
fi

# pf.conf may also load the anchor from a file inside the data directory, which
# step 5 has just removed. pfctl refuses the whole ruleset over a missing file,
# so the next reload — or the next boot — leaves the host with no rules at all.
# The documented setup needs no such line, but a host that has one has to be
# told before it finds out the hard way.
if [ "$KEEP_DATA" -eq 0 ] && [ -r /etc/pf.conf ]; then
	loaded=$(grep -oE "$DATA_DIR/[^\"[:space:]]*" /etc/pf.conf | head -1)
	if [ -n "$loaded" ] && [ ! -e "$loaded" ]; then
		echo
		echo "WARNING: /etc/pf.conf reads $loaded, which no longer exists."
		echo "PF will refuse the entire ruleset until that line is gone:"
		echo
		echo "    doas pfctl -n -f /etc/pf.conf   # says so without loading"
		echo
		echo "Remove the line that names it — hospitusd needs no 'load anchor ... from'"
		echo "and loads its own rules at runtime — then reload PF."
	fi
fi

step "Done"
echo
if [ -n "${PF_INCLUDE_KEPT:-}" ]; then
	echo "Kept $PF_INCLUDE_KEPT because /etc/pf.conf includes it. To finish, remove"
	echo "the include line from pf.conf first, then the file:"
	echo "    doas pfctl -n -f /etc/pf.conf   # check before loading"
	echo "    doas rm $PF_INCLUDE_KEPT"
	echo
fi

echo "Hospitus is removed. If you added the Hospitus anchor to your pf.conf, drop these"
echo "lines yourself and reload PF (Hospitus never edits pf.conf):"
echo '    nat-anchor "hospitus"'
echo '    rdr-anchor "hospitus"'
echo '    anchor "hospitus"'
