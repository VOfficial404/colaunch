# Colaunch's link on the user's own domain, run on the runtime by agent.py
# as the plugin "domain-<config>": a named Cloudflare Tunnel, configured in
# Cloudflare, whose token the app put in a file of the plugin's private
# folder that only its owner reads. The app puts D_TOKEN_FILE and D_MARK,
# then cloudflared.sh, above. Lines "@@DOMAIN ...@@" tell the app where it
# is; an "ERROR:" line, why it stopped. The token never goes on a command
# line, where any user of the machine would see it.
set -uo pipefail

echo "@@DOMAIN $D_MARK@@"
die() { echo "ERROR: $*"; exit 1; }

case "$D_TOKEN_FILE" in
  "$COLAUNCH_PLUGIN_DIR"/private/*) ;;
  *) die "the tunnel's token isn't in the plugin's private folder" ;;
esac
[ -f "$D_TOKEN_FILE" ] && [ ! -L "$D_TOKEN_FILE" ] || die "the tunnel's token is missing"
chmod 600 "$D_TOKEN_FILE" || die "can't make the token's file private"
# Stopping the plugin removes the token, as agent.py's stop does too.
trap 'rm -f "$D_TOKEN_FILE"' EXIT
trap 'exit 143' TERM INT

cf_ensure

echo "@@DOMAIN-RUN $D_MARK@@"
# cloudflared reads the token from a file with --token-file (2025.4.0 and
# later); older ones read TUNNEL_TOKEN from the environment.
if "$BIN" tunnel run --help 2>&1 | grep -q -- '--token-file'; then
  "$BIN" tunnel --no-autoupdate run --token-file "$D_TOKEN_FILE"
else
  TUNNEL_TOKEN="$(cat "$D_TOKEN_FILE")" "$BIN" tunnel --no-autoupdate run
fi
code=$?
die "cloudflared stopped (exit status $code)"
