# Colaunch's public link, run on the runtime by agent.py as the plugin
# "share-<port>": a Cloudflare quick tunnel to a port of the runtime. The app
# puts S_PORT and S_MARK, then cloudflared.sh, above. Lines "@@SHARE ...@@"
# tell the app where it is; an "ERROR:" line, why it stopped.
set -uo pipefail

echo "@@SHARE $S_MARK@@"
die() { echo "ERROR: $*"; exit 1; }

[[ "$S_PORT" =~ ^[0-9]{1,5}$ ]] || die "bad port $S_PORT"
cf_ensure

echo "@@SHARE-RUN $S_MARK@@"
exec "$BIN" tunnel --no-autoupdate --url "http://127.0.0.1:$S_PORT"
