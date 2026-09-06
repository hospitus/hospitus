#!/usr/bin/env bash
# Configure binmiscctl for cross-architecture jail support
#
# This script registers QEMU user-mode interpreters with FreeBSD's binmiscctl
# to enable running ARM64 and RISC-V binaries on AMD64 hosts.
#
# Prerequisites:
#   pkg install qemu-user-static
#
# Usage:
#   doas ./tools/setup-binmiscctl.sh

# Don't exit on error - we handle errors manually
# set -e

# Ensure the kernel module is loaded (ignore "already loaded" errors)
kldload imgact_binmisc 2>/dev/null || true

# Check if running as root
if [ "$(id -u)" -ne 0 ]; then
    echo "Error: This script must be run as root"
    echo "Usage: doas $0"
    exit 1
fi

# Check if binmiscctl is available
if ! command -v binmiscctl &> /dev/null; then
    echo "Error: binmiscctl not found. Is this FreeBSD?"
    exit 1
fi

# Reports whether an activator is registered *and* enabled, enabling it if it
# is merely registered. `binmiscctl lookup` succeeds for a disabled activator
# too, so matching the name alone would report success while no binary of that
# architecture actually runs.
activator_ready() {
    local arch="$1" info
    info=$(binmiscctl lookup "$arch" 2>/dev/null) || return 1
    case "$info" in
        *ENABLED*) return 0 ;;
    esac
    echo "[..] $arch is registered but disabled; enabling it"
    binmiscctl enable "$arch"
}

# --pre-open lets the kernel hold the interpreter open before the interpreted
# program starts, which is what makes it reachable from inside a jail or chroot
# without copying qemu-*-static into every one of them. binmiscctl(8): "To make
# the interpreter automatically available in jails and chroots, use the
# --pre-open option."
echo "Configuring binmiscctl for cross-architecture support..."
echo ""

# ARM64 (aarch64) configuration
if activator_ready aarch64; then
    echo "[OK] ARM64 (aarch64) registered and enabled"
else
    if [ -f "/usr/local/bin/qemu-aarch64-static" ]; then
        echo "Registering ARM64 (aarch64) interpreter..."
        binmiscctl add aarch64 \
            --interpreter "/usr/local/bin/qemu-aarch64-static" \
            --magic "\x7f\x45\x4c\x46\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\xb7\x00" \
            --mask "\xff\xff\xff\xff\xff\xff\xff\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff" \
            --size 20 \
            --set-enabled \
            --pre-open || {
            echo "[FAIL] registration failed; is qemu-user-static installed and binmiscctl available?" >&2
            exit 1
        }
        echo "[OK] ARM64 (aarch64) registered"
    else
        echo "[SKIP] ARM64 - qemu-aarch64-static not found"
        echo "       Install with: pkg install qemu-user-static"
    fi
fi

# RISC-V 64-bit configuration
if activator_ready riscv64; then
    echo "[OK] RISC-V 64 (riscv64) registered and enabled"
else
    if [ -f "/usr/local/bin/qemu-riscv64-static" ]; then
        echo "Registering RISC-V 64 (riscv64) interpreter..."
        binmiscctl add riscv64 \
            --interpreter "/usr/local/bin/qemu-riscv64-static" \
            --magic "\x7f\x45\x4c\x46\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\xf3\x00" \
            --mask "\xff\xff\xff\xff\xff\xff\xff\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff" \
            --size 20 \
            --set-enabled \
            --pre-open || {
            echo "[FAIL] registration failed; is qemu-user-static installed and binmiscctl available?" >&2
            exit 1
        }
        echo "[OK] RISC-V 64 (riscv64) registered"
    else
        echo "[SKIP] RISC-V 64 - qemu-riscv64-static not found"
        echo "       Install with: pkg install qemu-user-static"
    fi
fi

# ARM 32-bit (armv7) configuration
if activator_ready armv7; then
    echo "[OK] ARM 32-bit (armv7) registered and enabled"
else
    if [ -f "/usr/local/bin/qemu-arm-static" ]; then
        echo "Registering ARM 32-bit (armv7) interpreter..."
        binmiscctl add armv7 \
            --interpreter "/usr/local/bin/qemu-arm-static" \
            --magic "\x7f\x45\x4c\x46\x01\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x28\x00" \
            --mask "\xff\xff\xff\xff\xff\xff\xff\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff" \
            --size 20 \
            --set-enabled \
            --pre-open || {
            echo "[FAIL] registration failed; is qemu-user-static installed and binmiscctl available?" >&2
            exit 1
        }
        echo "[OK] ARM 32-bit (armv7) registered"
    else
        echo "[SKIP] ARM 32-bit - qemu-arm-static not found"
    fi
fi

# i386 on amd64 is native, no configuration needed
echo "[INFO] i386 (32-bit x86) is native on amd64, no emulation needed"

echo ""
echo "Current binmiscctl configuration:"
echo "=================================="
binmiscctl list 2>/dev/null || echo "(No entries registered)"

echo ""
echo "To make this configuration persistent across reboots, add to /etc/rc.conf:"
echo '  binmiscctl_enable="YES"'
echo ""
echo "And create /etc/rc.d/binmiscctl.local or use /etc/rc.local to run this script."
