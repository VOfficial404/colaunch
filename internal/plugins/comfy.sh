# Colaunch's ComfyUI plugin, run on the runtime by agent.py. The app puts
# its settings in C_* variables above. Lines "@@PHASE name@@" tell the app
# what it's doing.
set -uo pipefail

phase() { echo "@@PHASE $1@@"; }
die() { echo "ERROR: $*"; exit 1; }

# The Hugging Face token comes, when it may, in a file of its own that
# only root reads: it's read for the downloads, removed after them, and
# ComfyUI never gets it. A token from elsewhere isn't used.
unset HF_TOKEN HUGGING_FACE_HUB_TOKEN
HF_TOKEN_VALUE=""
forget_token() {
  [ -n "${C_HF_TOKEN_FILE:-}" ] && rm -f "$C_HF_TOKEN_FILE"
  HF_TOKEN_VALUE=""
}

# Keep the watchdog from taking downloads and installs for idleness.
( while true; do touch "$COLAUNCH_BUSY"; sleep 30; done ) &
BUSY_PID=$!
trap 'kill $BUSY_PID 2>/dev/null; forget_token' EXIT

# Kept between deployments on the same runtime: ComfyUI, its packages'
# stamp, and the models, which the next deployment reuses.
ROOT="$COLAUNCH_HOME/comfy"
APP="$ROOT/ComfyUI"
MODELS="$APP/models"
mkdir -p "$ROOT"

is_sha256() { [[ "$1" =~ ^[0-9a-fA-F]{64}$ ]]; }

# ------------------------------------------------------------ ComfyUI

phase install
# git_at DIR URL REF COMMIT puts the repository at URL in DIR, at COMMIT:
# a shallow clone of REF, a tag, or of COMMIT itself when REF is "".
git_at() {
  local dir="$1" url="$2" ref="$3" commit="$4"
  if [ -d "$dir/.git" ] && [ "$(git -C "$dir" rev-parse HEAD 2>/dev/null)" = "$commit" ]; then
    return 0
  fi
  rm -rf "$dir"
  if [ -n "$ref" ]; then
    git -c advice.detachedHead=false clone -q --depth 1 --branch "$ref" "$url" "$dir" || return 1
  else
    git init -q "$dir" && git -C "$dir" remote add origin "$url" &&
      git -C "$dir" fetch -q --depth 1 origin "$commit" &&
      git -c advice.detachedHead=false -C "$dir" checkout -q FETCH_HEAD || return 1
  fi
  local got
  got="$(git -C "$dir" rev-parse HEAD)"
  if [ "$got" != "$commit" ]; then
    echo "$url $ref is at $got, not $commit as expected: not running it."
    rm -rf "$dir"
    return 1
  fi
}

# The models of an earlier deployment stay while ComfyUI is replaced.
if [ -d "$APP" ] && [ "$(git -C "$APP" rev-parse HEAD 2>/dev/null)" != "$C_COMFY_COMMIT" ]; then
  if [ -d "$MODELS" ]; then mv "$MODELS" "$ROOT/models.keep"; fi
fi
echo "ComfyUI $C_COMFY_TAG"
git_at "$APP" "$C_COMFY_UPSTREAM" "$C_COMFY_TAG" "$C_COMFY_COMMIT" || die "couldn't get ComfyUI $C_COMFY_TAG"
if [ -d "$ROOT/models.keep" ]; then
  rm -rf "$MODELS" && mv "$ROOT/models.keep" "$MODELS"
fi
echo "ComfyUI-GGUF ${C_GGUF_COMMIT:0:7}"
git_at "$APP/custom_nodes/ComfyUI-GGUF" "$C_GGUF_UPSTREAM" "" "$C_GGUF_COMMIT" || die "couldn't get ComfyUI-GGUF"

# Colab's Python has PyTorch built for its GPU: it stays. Its packages are
# left out of what's installed, and held at their versions so that no
# other package replaces them.
STAMP="$ROOT/.packages-$C_COMFY_COMMIT-$C_GGUF_COMMIT"
if [ ! -f "$STAMP" ]; then
  python3 -c "import torch" 2>/dev/null || die "this runtime's Python has no PyTorch, which Colab's GPU runtimes come with"
  CONSTRAINTS="$ROOT/constraints.txt"
  python3 -m pip list --format=freeze 2>/dev/null | grep -iE '^(torch|torchvision|torchaudio|triton)==' >"$CONSTRAINTS"
  echo "Keeping $(tr '\n' ' ' <"$CONSTRAINTS")"
  REQS="$ROOT/requirements.txt"
  cat "$APP/requirements.txt" "$APP/custom_nodes/ComfyUI-GGUF/requirements.txt" |
    grep -viE '^[[:space:]]*(torch|torchvision|torchaudio)[[:space:]]*([<>=!~;[]|$)' >"$REQS"
  python3 -m pip install -q --disable-pip-version-check -r "$REQS" -c "$CONSTRAINTS" 2>&1 | grep -v "^ *$" | tail -20
  [ "${PIPESTATUS[0]}" = 0 ] || die "couldn't install ComfyUI's packages"
  python3 -c "import torch, gguf, av, aiohttp" || die "ComfyUI's packages don't import"
  touch "$STAMP"
fi
python3 -c "import torch; print('PyTorch', torch.__version__, 'CUDA', torch.version.cuda, 'GPU', torch.cuda.is_available())"

# ------------------------------------------------------------ models

phase download
export HF_ENDPOINT="$C_HF_ENDPOINT"
if [ -n "$C_HF_TOKEN_FILE" ] && [ -f "$C_HF_TOKEN_FILE" ]; then
  HF_TOKEN_VALUE="$(cat "$C_HF_TOKEN_FILE")"
fi
# hf_curl is curl with the token, if any, in a header that goes through a
# pipe, not on the command line, which others can read.
hf_curl() {
  if [ -n "$HF_TOKEN_VALUE" ]; then
    curl -K <(printf 'header = "Authorization: Bearer %s"\n' "$HF_TOKEN_VALUE") "$@"
  else
    curl "$@"
  fi
}

# download URL SHA256 SIZE DEST TOKEN puts URL at models/DEST, checked
# against SHA256 ("-" when Hugging Face gives none). TOKEN is 1 for the
# user's own repositories, which may need it; others never get it.
download() {
  local url="$1" sum="$2" size="$3" dest="$MODELS/$4" token="$5"
  case "$4" in
    */../* | ../* | */.. | /*) die "unsafe path $4" ;;
  esac
  [ "$sum" = - ] && sum=""
  if [ -n "$sum" ] && ! is_sha256 "$sum"; then die "bad checksum for $4"; fi
  if [ -s "$dest" ] && [ ! -e "$dest.part" ]; then
    if [ -z "$sum" ] || [ "$(cat "$dest.sha256" 2>/dev/null)" = "$sum" ]; then
      echo "Have $4"
      return 0
    fi
  fi
  mkdir -p "$(dirname "$dest")"
  echo "Downloading $4 ($((size / 1048576)) MiB)"
  touch "$dest.part"
  local fetch=curl
  [ "$token" = 1 ] && fetch=hf_curl
  local try
  for try in 1 2 3; do
    # Sample progress redraws without discarding curl's actual error lines.
    $fetch -fL --retry 5 -C - -o "$dest" "$url" --progress-bar 2>&1 |
      awk 'BEGIN { RS = "[\r\n]" } /^curl:|^Warning:/ || (NF && NR % 40 == 0) { print; fflush() }'
    local result="${PIPESTATUS[0]}"
    [ "$result" = 0 ] && break
    echo "Download attempt $try failed for $4 (curl exit $result)"
    [ "$try" = 3 ] && { rm -f "$dest" "$dest.part" "$dest.sha256"; return 1; }
    echo "Retrying $4"
  done
  if [ -n "$sum" ]; then
    local got
    got="$(sha256sum "$dest" | awk '{print $1}')"
    if [ "${got,,}" != "${sum,,}" ]; then
      echo "Checksum mismatch for $4: sha256 $got, expected $sum. Not using it."
      rm -f "$dest" "$dest.part" "$dest.sha256"
      return 1
    fi
    echo "Checksum verified: sha256 $got"
    echo "$sum" >"$dest.sha256"
  else
    echo "WARNING: Hugging Face gives no checksum for $4: it isn't verified."
  fi
  rm -f "$dest.part"
}

FREE="$(df -Pk "$ROOT" | awk 'NR==2{print $4 * 1024}')"
NEED=0
while read -r url sum size dest token; do
  [ -n "$url" ] || continue
  [ -s "$MODELS/$dest" ] || NEED=$((NEED + size))
done <<<"$C_DOWNLOADS"
if [ "$NEED" -gt "$FREE" ]; then
  die "the downloads need $((NEED / 1073741824)) GB of disk, and the runtime has $((FREE / 1073741824)) GB free"
fi
while read -r url sum size dest token; do
  [ -n "$url" ] || continue
  download "$url" "$sum" "$size" "$dest" "$token" || die "couldn't download $dest"
done <<<"$C_DOWNLOADS"
forget_token
unset HF_TOKEN_VALUE C_HF_TOKEN_FILE HF_ENDPOINT

# What was deployed, which the app reads at /userdata/colaunch.json, and
# the family's workflow, which the web page's workflows list has.
USERDIR="$APP/user/default"
mkdir -p "$USERDIR/workflows"
printf '%s' "$C_DEPLOYMENT" | base64 -d >"$USERDIR/colaunch.json" || die "couldn't write the deployment"
printf '%s' "$C_WORKFLOW" | base64 -d >"$USERDIR/workflows/colaunch-$C_FAMILY.json" || die "couldn't write the workflow"
echo "Workflow: workflows/colaunch-$C_FAMILY.json"

# ------------------------------------------------------------ serve

phase start
kill $BUSY_PID 2>/dev/null
cd "$APP" || die "no ComfyUI"
ARGS=(main.py --listen 127.0.0.1 --port "$C_PORT" --disable-auto-launch --preview-method auto)
[ -n "$C_OFFLOAD" ] && ARGS+=(--lowvram)
echo "python3 ${ARGS[*]}"
unset C_DOWNLOADS C_DEPLOYMENT C_WORKFLOW
exec python3 "${ARGS[@]}"
