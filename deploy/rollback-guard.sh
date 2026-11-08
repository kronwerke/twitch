#!/bin/sh
# Runs before every start of kronwerke-twitch (ExecStartPre). After a self update the service
# writes state/update.trial ("<starts> <tag>"); the service deletes it once it reaches
# Twitch. If a new binary fails three starts in a row, the previous one is put back
# and its tag is written to state/update.skip so it is not installed again.
set -eu
dir=${KW_DIR:-/srv/projects/kronwerke-twitch}
trial=$dir/state/update.trial
[ -f "$trial" ] || exit 0
read -r starts tag < "$trial" || true
starts=$(( ${starts:-0} + 1 ))
echo "$starts ${tag:-}" > "$trial"
if [ "$starts" -gt 3 ] && [ -f "$dir/bin/kronwerke-twitch.prev" ]; then
  mv "$dir/bin/kronwerke-twitch.prev" "$dir/bin/kronwerke-twitch"
  echo "${tag:-unknown}" > "$dir/state/update.skip"
  rm -f "$trial"
  echo "rollback-guard: ${tag:-the new binary} failed three starts, the previous binary is back" >&2
fi
exit 0
