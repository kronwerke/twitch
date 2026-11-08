#!/bin/sh
# One-time installation of kronwerke-twitch on a systemd server. Run as root from the
# folder this script is in, next to the files of a release: the binary
# (kronwerke-twitch-linux-amd64 or kronwerke-twitch), rollback-guard.sh,
# kronwerke-twitch.service and config.example.env. Later versions arrive by themselves
# through the service's self update. Running it again keeps twitch.env and the state.
set -eu
cd "$(dirname "$0")"
dir=/srv/projects/kronwerke-twitch
bin=kronwerke-twitch
[ -f "$bin" ] || bin=kronwerke-twitch-linux-amd64

id kronwerke-twitch >/dev/null 2>&1 || useradd --system --home-dir "$dir" --shell /usr/sbin/nologin kronwerke-twitch
install -d -o kronwerke-twitch -g kronwerke-twitch -m 750 "$dir" "$dir/bin" "$dir/state"
install -o kronwerke-twitch -g kronwerke-twitch -m 755 "$bin" "$dir/bin/kronwerke-twitch"
install -d -m 755 /usr/local/lib/kronwerke-twitch
install -o root -g root -m 755 rollback-guard.sh /usr/local/lib/kronwerke-twitch/rollback-guard.sh
install -d -o root -g kronwerke-twitch -m 750 /etc/kronwerke-twitch
[ -f /etc/kronwerke-twitch/twitch.env ] || install -o root -g kronwerke-twitch -m 640 config.example.env /etc/kronwerke-twitch/twitch.env
install -o root -g root -m 644 kronwerke-twitch.service /etc/systemd/system/kronwerke-twitch.service
cat > "$dir/project.manifest" <<MANIFEST
name = kronwerke-twitch
owner = private
ports = 13040-13049
service = kronwerke-twitch.service
data = $dir/state
config = /etc/kronwerke-twitch/twitch.env
MANIFEST
chown kronwerke-twitch:kronwerke-twitch "$dir/project.manifest"
systemctl daemon-reload
echo "installed $("$dir/bin/kronwerke-twitch" version)"
