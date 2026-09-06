#!/bin/sh
#
# Put a file into Nextcloud, take it back out, and check where it went.
#
# The health checks already say the stack answers. This says it keeps what it
# is given, and that the separation the manifest is built around is real: the
# file must arrive on the data volume, and nothing under the web root may lead
# to it.
#
# Run by the manifest test harness once the health checks pass. It addresses
# instances by the role the manifest gives them, because the created names
# carry a per-run prefix:
#
#   HOSPITUS_BIN         path to the hospitus CLI
#   HOSPITUS_ROLE_APP    the application jail
#   HOSPITUS_ROLE_WEB    the web jail
#
# Usable by hand too:
#   HOSPITUS_BIN=hospitus HOSPITUS_ROLE_APP=nc_app HOSPITUS_ROLE_WEB=nc_web sh seed.sh

set -eu

: "${HOSPITUS_BIN:?set HOSPITUS_BIN to the hospitus CLI}"
: "${HOSPITUS_ROLE_APP:?set HOSPITUS_ROLE_APP to the application jail}"
: "${HOSPITUS_ROLE_WEB:?set HOSPITUS_ROLE_WEB to the web jail}"

NC_USER=seeded-user
PASS=seeded-passphrase-0
FILE=hello-from-the-seed.txt
BODY='the quick brown fox'

app() { "$HOSPITUS_BIN" jail exec "$HOSPITUS_ROLE_APP" "$@"; }
web() { "$HOSPITUS_BIN" jail exec "$HOSPITUS_ROLE_WEB" "$@"; }
occ() { app su -m www -c "/usr/local/bin/php /usr/local/www/nextcloud/occ $*"; }

cleanup() {
	occ "user:delete $NC_USER" >/dev/null 2>&1 || true
	# The netrc holds the seed password: it goes with the user it belongs to,
	# on every exit path.
	web sh -c "rm -f /tmp/.seed-netrc" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

echo "==> creating a user"
# occ reads the password from OC_PASS rather than from its own argv, so it does
# not show up in occ's process arguments. The assignment is still inside the
# string this script hands to sh, so it is visible in that shell's arguments
# for as long as it runs — acceptable for a throwaway seed credential on a
# test host, and not a pattern to copy for a real password.
app sh -c "OC_PASS='$PASS' su -m www -c \
	'/usr/local/bin/php /usr/local/www/nextcloud/occ user:add --password-from-env $NC_USER'"

echo "==> making sure the web tier can speak WebDAV"
# fetch(1) reads and nothing else: it has no PUT, which WebDAV needs to store
# a file. curl is the smallest tool that does both, and this script installs
# it rather than the manifest, because the deployment itself has no use for it.
# stdout is silenced, stderr is not: a failed install used to leave the next
# command reporting "curl: not found" with no trace of why.
web sh -c "command -v curl >/dev/null || pkg install -y curl >/dev/null"

echo "==> uploading a file through the web tier"
# Through the front, not straight to disk: one request exercises Caddy,
# PHP-FPM and the database together, which is the path a real client takes.
web sh -c "printf '%s' '$BODY' > /tmp/$FILE"
# The credentials go into a netrc read by curl, not onto curl's command line:
# every curl below would otherwise carry them in its own arguments, once per
# request.
web sh -c "umask 077; printf 'machine 127.0.0.1 login %s password %s\n' '$NC_USER' '$PASS' > /tmp/.seed-netrc"
web sh -c "curl -sSf -m 30 --netrc-file /tmp/.seed-netrc -T /tmp/$FILE \
	'http://127.0.0.1:80/remote.php/dav/files/$NC_USER/$FILE'"

echo "==> reading it back"
got=$(web sh -c "curl -sSf -m 30 --netrc-file /tmp/.seed-netrc \
	'http://127.0.0.1:80/remote.php/dav/files/$NC_USER/$FILE'")

if [ "$got" != "$BODY" ]; then
	echo "read back '$got', wrote '$BODY'" >&2
	exit 1
fi
echo "    content matches"

echo "==> checking where the file landed"
# It belongs on the data volume, which is a different dataset from the code.
if ! app sh -c "test -f /var/db/nextcloud-data/$NC_USER/files/$FILE"; then
	echo "the file is not on the data volume: the separation is not real" >&2
	app sh -c "ls -la /var/db/nextcloud-data/$NC_USER/files/ 2>&1 | head" >&2
	exit 1
fi
echo "    stored under /var/db/nextcloud-data, off the web root"

# The package ships an empty data/ inside the web root, so its presence proves
# nothing either way. What matters is where Nextcloud actually writes, and
# whether the front will hand the file out as a static path — which is the
# failure that keeping the two apart exists to prevent.
configured=$(occ "config:system:get datadirectory" | tr -d '\r\n')
if [ -z "$configured" ]; then
	# An empty answer is not "the path is fine": occ failed, and the case
	# below would then match nothing and let the check pass.
	echo "could not read the configured data directory" >&2
	exit 1
fi
case "$configured" in
/usr/local/www/nextcloud*)
	echo "the data directory is inside the web root: $configured" >&2
	exit 1
	;;
esac
echo "    Nextcloud writes to $configured"

if web sh -c "curl -sf -m 15 -o /dev/null 'http://127.0.0.1:80/data/$NC_USER/files/$FILE'"; then
	echo "the web tier served a user's file as a static path" >&2
	exit 1
fi
echo "    the front will not serve it as a static path"

echo "==> the database holds the account"
occ "user:info $NC_USER" | grep -q "$NC_USER"
echo "    user:info answers, so the row survived the round trip"
