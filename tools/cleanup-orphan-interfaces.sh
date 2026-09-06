#!/usr/bin/env bash
# Destroy orphaned epair and tap interfaces not associated with any running jail or VM.
# Run as root after stopping all hospitus jails/VMs.
#
# Usage: doas sh tools/cleanup-orphan-interfaces.sh [--dry-run]

set -euo pipefail

DRY_RUN=0
for arg in "$@"; do
    case "$arg" in
        --dry-run) DRY_RUN=1 ;;
        # This script destroys interfaces. A typo must not be read as consent
        # to run for real, so anything unrecognized stops it.
        *) echo "unknown option: $arg" >&2
           echo "usage: $0 [--dry-run]" >&2
           exit 2 ;;
    esac
done

# Build set of interfaces currently in use by running jails
JAIL_IFACES=""
if jls -q name 2>/dev/null | grep -q .; then
    for jail in $(jls -q name 2>/dev/null); do
        ifaces=$(jexec "$jail" ifconfig -l 2>/dev/null || true)
        JAIL_IFACES="$JAIL_IFACES $ifaces"
    done
fi

destroy_if() {
    local iface="$1"
    echo "Destroying orphaned interface: $iface"
    if [ "$DRY_RUN" -eq 0 ]; then
        # Remove from any bridge first to avoid ifconfig destroy hanging
        for bridge in $(ifconfig -g bridge 2>/dev/null); do
            ifconfig "$bridge" deletem "$iface" 2>/dev/null || true
        done
        ifconfig "$iface" destroy 2>/dev/null || echo "  (already gone)"
    fi
}

# Collect all epairNa interfaces (destroying epairNa also destroys epairNb)
for iface in $(ifconfig -l 2>/dev/null | tr ' ' '\n' | grep '^epair[0-9]*a$'); do
    # Check if epairNb (jail side) is in use by a running jail
    epair_b="${iface%a}b"
    if echo "$JAIL_IFACES" | grep -qw "$epair_b"; then
        echo "Skipping $iface — $epair_b is in use by a running jail"
        continue
    fi
    destroy_if "$iface"
done

# Collect all tap_* interfaces. A tap opened by a running bhyve process is that
# VM's network card; destroying it cuts the guest off with no error anywhere.
for iface in $(ifconfig -l 2>/dev/null | tr ' ' '\n' | grep '^tap_'); do
    if ifconfig "$iface" 2>/dev/null | grep -q 'Opened by PID'; then
        echo "Skipping $iface — opened by a running VM"
        continue
    fi
    destroy_if "$iface"
done

echo "Done."
