# Fetching cloudflared, shared by Colaunch's public links (share.sh) and
# links on the user's own domain (domain.sh): functions only, which the
# script after it calls once it has printed its mark and defined die.

CF_DIR="$COLAUNCH_HOME/cloudflared"
BIN="$CF_DIR/cloudflared"
CF_API="https://api.github.com/repos/cloudflare/cloudflared/releases/latest"
CF_ASSET="cloudflared-linux-amd64"

# cf_asset reads the release's JSON and prints "address sha256" of
# CF_ASSET, the sha256 from the asset's digest, "" when GitHub gives none.
cf_asset() {
  python3 -c '
import json, sys
rel = json.load(sys.stdin)
for a in rel.get("assets") or []:
    if a.get("name") == sys.argv[1]:
        d = a.get("digest") or ""
        print(a.get("browser_download_url") or "", d[len("sha256:"):] if d.startswith("sha256:") else "")
        break
' "$CF_ASSET" 2>/dev/null
}

cf_download() {
  echo "Looking up cloudflared's latest release"
  local url="" want="" got tmp
  read -r url want < <(curl -fsSL --retry 3 -H "Accept: application/vnd.github+json" "$CF_API" | cf_asset)
  want="${want%$'\r'}"
  [ -n "$url" ] || die "no $CF_ASSET in cloudflared's latest release"
  [[ "$url" == https://github.com/cloudflare/cloudflared/releases/download/* ]] || die "unexpected address $url"
  [[ "$want" =~ ^[0-9a-fA-F]{64}$ ]] || die "GitHub gives no sha256 for $CF_ASSET: not running it unverified"
  echo "Downloading $url"
  tmp="$(mktemp "$CF_DIR/.download.XXXXXX")" || die "can't write in $CF_DIR"
  if ! curl -fL --retry 3 -sS -o "$tmp" "$url"; then
    rm -f "$tmp"
    die "downloading cloudflared failed"
  fi
  got="$(sha256sum "$tmp" | awk '{print $1}')"
  if [ "${got,,}" != "${want,,}" ]; then
    rm -f "$tmp"
    die "checksum mismatch for $CF_ASSET: sha256 $got, expected $want. Not running it."
  fi
  echo "Checksum verified: sha256 $got"
  chmod 700 "$tmp" && mv -f "$tmp" "$BIN" || { rm -f "$tmp"; die "can't put cloudflared in place"; }
}

# cf_ensure has a verified cloudflared at BIN that runs here.
cf_ensure() {
  [ "$(uname -m)" = x86_64 ] || die "cloudflared is fetched for Linux amd64 only, not $(uname -m)"
  mkdir -p "$CF_DIR" && chmod 700 "$CF_DIR" || die "can't make $CF_DIR"
  [ -x "$BIN" ] || cf_download
  "$BIN" --version || { rm -f "$BIN"; die "cloudflared doesn't run here"; }
}
