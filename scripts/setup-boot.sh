#!/bin/sh

# HOSPITUS - Boot & Persistence Setup Script
#
# Installs the hospitus rc.d service and configures it to start at boot.
# This is the convenience path for source installs; the FreeBSD port
# (sysutils/hospitus) installs the same rc.d script automatically.

set -e

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root or with doas."
    exit 1
fi

# Resolve repository root so the script works from any working directory.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)
# Where packages live: LOCALBASE is a ports make variable, unset in a plain
# shell, so ask the running system.
LOCALBASE="${LOCALBASE:-$(sysctl -n user.localbase 2>/dev/null || echo /usr/local)}"
RC_SRC="${REPO_ROOT}/etc/rc.d/hospitus"
RC_DST="${LOCALBASE}/etc/rc.d/hospitus"

if [ ! -f "${RC_SRC}" ]; then
    echo "Error: rc.d script not found at ${RC_SRC}"
    exit 1
fi

echo "🚀 Starting HOSPITUS boot configuration..."

# 1. Install rc.d script (service name: hospitus)
echo "📦 Installing rc.d service to ${RC_DST}..."
install -m 0555 "${RC_SRC}" "${RC_DST}"

# 2. Configure rc.conf using sysrc (variables consumed by etc/rc.d/hospitus)
echo "⚙️  Configuring service parameters in /etc/rc.conf..."
sysrc hospitus_enable="YES"
sysrc hospitus_data_dir="/var/lib/hospitus"
sysrc hospitus_state_dir="/var/lib/hospitus/state"
sysrc hospitus_db="/var/lib/hospitus/hospitus.db"

# 3. Ensure data directories exist with restrictive permissions
echo "📁 Ensuring data directories exist..."
install -d -m 0750 /var/lib/hospitus
install -d -m 0700 /var/lib/hospitus/state

# 4. Start the daemon (generates an API key on first start if none exists)
echo "📡 Starting hospitus service..."
# Stop first so re-running the script picks up the new configuration, but
# quietly: on a first install there is nothing to stop, and rc.subr would
# announce "hospitus not running? (check /var/run/hospitus.pid)" before starting it,
# which reads like a failure at the exact moment the install succeeded.
service hospitus stop >/dev/null 2>&1 || true
service hospitus start

echo ""
echo "✅ Configuration complete!"
echo "--------------------------------------------------"
echo "Summary:"
echo " - The hospitus service will start automatically at boot."
echo " - Service parameters are configured in /etc/rc.conf (hospitus_*)."
echo " - On first start an API key was generated at"
echo "   ${LOCALBASE}/etc/hospitus/api.key (see 'service hospitus start' output)."
echo ""
echo "💡 To enable auto-start for a specific instance, run:"
echo "   hospitus jail set <name> --auto-start"
echo "--------------------------------------------------"
