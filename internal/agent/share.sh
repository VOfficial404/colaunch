# Colaunch's public link, run on the runtime by agent.py as the plugin
# "share-<port>": a Cloudflare quick tunnel to a port of the runtime. The app
# puts S_PORT and S_MARK above. Lines "@@SHARE ...@@" tell the app where it
# is; an "ERROR:" line, why it stopped.
set -uo pipefail

echo "@@SHARE $S_MARK@@"
die() { echo "ERROR: $*"; exit 1; }

[[ "$S_PORT" =~ ^[0-9]{1,5}$ ]] || die "bad port $S_PORT"
[ "$(uname -m)" = x86_64 ] || die "cloudflared is fetched for Linux amd64 only, not $(uname -m)"

DIR="$COLAUNCH_HOME/cloudflared"
BIN="$DIR/cloudflared"
API="https://api.github.com/repos/cloudflare/cloudflared/releases/latest"
ASSET="cloudflared-linux-amd64"
mkdir -p "$DIR" && chmod 700 "$DIR" || die "can't make $DIR"

# asset reads the release's JSON and prints "address sha256" of ASSET, the
# sha256 from the asset's digest, "" when GitHub gives none.
asset() {
  python3 -c '
import json, sys
rel = json.load(sys.stdin)
for a in rel.get("assets") or []:
    if a.get("name") == sys.argv[1]:
        d = a.get("digest") or ""
        print(a.get("browser_download_url") or "", d[len("sha256:"):] if d.startswith("sha256:") else "")
        break
' "$ASSET" 2>/dev/null
}

download() {
  echo "Looking up cloudflared's latest release"
  local url="" want="" got tmp
  read -r url want < <(curl -fsSL --retry 3 -H "Accept: application/vnd.github+json" "$API" | asset)
  want="${want%$'\r'}"
  [ -n "$url" ] || die "no $ASSET in cloudflared's latest release"
  [[ "$url" == https://github.com/cloudflare/cloudflared/releases/download/* ]] || die "unexpected address $url"
  [[ "$want" =~ ^[0-9a-fA-F]{64}$ ]] || die "GitHub gives no sha256 for $ASSET: not running it unverified"
  echo "Downloading $url"
  tmp="$(mktemp "$DIR/.download.XXXXXX")" || die "can't write in $DIR"
  if ! curl -fL --retry 3 -sS -o "$tmp" "$url"; then
    rm -f "$tmp"
    die "downloading cloudflared failed"
  fi
  got="$(sha256sum "$tmp" | awk '{print $1}')"
  if [ "${got,,}" != "${want,,}" ]; then
    rm -f "$tmp"
    die "checksum mismatch for $ASSET: sha256 $got, expected $want. Not running it."
  fi
  echo "Checksum verified: sha256 $got"
  chmod 700 "$tmp" && mv -f "$tmp" "$BIN" || { rm -f "$tmp"; die "can't put cloudflared in place"; }
}

[ -x "$BIN" ] || download
"$BIN" --version || { rm -f "$BIN"; die "cloudflared doesn't run here"; }

echo "@@SHARE-RUN $S_MARK@@"
exec "$BIN" tunnel --no-autoupdate --url "http://127.0.0.1:$S_PORT"
