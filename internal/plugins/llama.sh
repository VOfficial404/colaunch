# Colaunch's llama.cpp plugin, run on the runtime by agent.py. The app puts
# its settings in L_* variables above. Lines "@@PHASE name@@" tell the app
# what it's doing.
set -uo pipefail

phase() { echo "@@PHASE $1@@"; }
die() { echo "ERROR: $*"; exit 1; }

# The Hugging Face token comes, when it may, in a file of its own that
# only root reads: it's read for the downloads, removed after them, and
# llama-server never gets it. A token from elsewhere isn't used.
unset HF_TOKEN HUGGING_FACE_HUB_TOKEN
HF_TOKEN_VALUE=""
forget_token() {
  [ -n "${L_HF_TOKEN_FILE:-}" ] && rm -f "$L_HF_TOKEN_FILE"
  HF_TOKEN_VALUE=""
}

# Keep the watchdog from taking downloads and builds for idleness.
( while true; do touch "$COLAUNCH_BUSY"; sleep 30; done ) &
BUSY_PID=$!
trap 'kill $BUSY_PID 2>/dev/null; forget_token' EXIT

ROOT="$COLAUNCH_HOME/llama"
MODELS="$COLAUNCH_HOME/models"
mkdir -p "$ROOT" "$MODELS"
ARCH="$(uname -m)"
# Where Colab keeps CUDA's libraries, which the prebuilt server links to.
export LD_LIBRARY_PATH="/usr/local/cuda/lib64:/usr/lib64-nvidia:${LD_LIBRARY_PATH:-}"

works() { "$1" --version >/dev/null 2>&1; }

# ------------------------------------------------------------ checking

# release_json URL prints a release from GitHub's API.
release_json() {
  curl -fsSL --retry 3 -H "Accept: application/vnd.github+json" "$1"
}

# gh_release reads a release's JSON and prints its tag (gh_release tag),
# or "address sha256" of its asset named NAME (gh_release name NAME) or of
# the first one ending in a SUFFIX (gh_release suffix SUFFIX...). The
# sha256 is the asset's digest, "" when GitHub gives none.
gh_release() {
  python3 -c '
import json, sys
rel = json.load(sys.stdin)
what, args = sys.argv[1], sys.argv[2:]
if what == "tag":
    print(rel.get("tag_name") or "")
    sys.exit(0)
for a in rel.get("assets") or []:
    n = a.get("name") or ""
    if (what == "name" and n == args[0]) or (what == "suffix" and any(n.endswith(s) for s in args)):
        d = a.get("digest") or ""
        print(a.get("browser_download_url") or "", d[len("sha256:"):] if d.startswith("sha256:") else "")
        break
' "$@" 2>/dev/null
}

is_sha256() { [[ "$1" =~ ^[0-9a-fA-F]{64}$ ]]; }

# check_sum FILE SHA256 NAME
check_sum() {
  local got
  got="$(sha256sum "$1" | awk '{print $1}')"
  if [ "${got,,}" != "${2,,}" ]; then
    echo "Checksum mismatch for $3: sha256 $got, expected $2. Not using it."
    return 1
  fi
  echo "Checksum verified: sha256 $got"
}

# safe_extract ARCHIVE DIR unpacks a .tar.gz or .zip into DIR: regular
# files only, none outside DIR. A link to a file beside it, as libraries'
# versioned names are, becomes a copy of that file; other links, hard
# links and devices are left out.
safe_extract() {
  python3 - "$1" "$2" <<'PY'
import os, shutil, stat, sys, tarfile, zipfile

src, dest = sys.argv[1], sys.argv[2]
os.makedirs(dest, exist_ok=True)
dest = os.path.realpath(dest)


def where(name):
    name = name.replace("\\", "/")
    parts = [p for p in name.split("/") if p not in ("", ".")]
    if name.startswith("/") or not parts or ".." in parts or ":" in parts[0]:
        sys.exit("unsafe path in the archive: %r" % name)
    return os.path.join(dest, *parts)


def write(path, data, mode):
    folder = os.path.dirname(path)
    os.makedirs(folder, mode=0o755, exist_ok=True)
    # Nothing here makes links, but make sure no folder leads out.
    if os.path.commonpath([dest, os.path.realpath(folder)]) != dest:
        sys.exit("unsafe path in the archive: %r" % path)
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, 0o755 if mode & 0o111 else 0o644)
    with os.fdopen(fd, "wb") as f:
        shutil.copyfileobj(data, f, 1 << 20)


links = []
if zipfile.is_zipfile(src):
    with zipfile.ZipFile(src) as z:
        for info in z.infolist():
            if info.is_dir():
                continue
            path = where(info.filename)
            mode = info.external_attr >> 16
            if stat.S_ISLNK(mode):
                links.append((path, z.read(info).decode("utf-8", "replace")))
            elif mode == 0 or stat.S_ISREG(mode):
                with z.open(info) as data:
                    write(path, data, mode or 0o644)
else:
    with tarfile.open(src, "r:*") as t:
        for m in t:
            if m.isdir():
                continue
            path = where(m.name)
            if m.issym():
                links.append((path, m.linkname))
            elif m.isfile():
                write(path, t.extractfile(m), m.mode)

for _ in range(len(links)):
    rest = []
    for path, target in links:
        if "/" in target or "\\" in target or target in ("", ".", ".."):
            continue
        source = os.path.join(os.path.dirname(path), target)
        if os.path.isfile(source) and not os.path.islink(source):
            with open(source, "rb") as data:
                write(path, data, os.stat(source).st_mode)
        else:
            rest.append((path, target))
    if len(rest) == len(links):
        break
    links = rest
PY
}

# fetch URL FILE SHA256 NAME downloads URL to FILE and checks it against
# SHA256, unless that's "", for an address of the settings that has no
# checksum.
fetch() {
  rm -f "$2"
  curl -fL --retry 3 -sS -o "$2" "$1" || { rm -f "$2"; return 1; }
  if [ -n "$3" ]; then
    check_sum "$2" "$3" "$4" || { rm -f "$2"; return 1; }
  fi
}

LATEST=""
# latest_release keeps llama.cpp's latest release in LATEST.
latest_release() {
  [ -n "$LATEST" ] || LATEST="$(release_json https://api.github.com/repos/ggml-org/llama.cpp/releases/latest 2>/dev/null)"
  [ -n "$LATEST" ]
}

# ------------------------------------------------------------ llama.cpp

get_cuda_prebuilt() {
  local dir="$ROOT/cuda"
  [ -f "$dir/.ok" ] && [ -x "$dir/llama-server" ] && works "$dir/llama-server" && { SERVER="$dir/llama-server"; return 0; }
  [ -n "$L_CUDA_URL" ] && [ "$ARCH" = x86_64 ] || return 1
  # The checksum: the asset's digest in GitHub's API, else the .sha256
  # published beside it.
  local want=""
  if [ -n "$L_CUDA_API" ]; then
    want="$(release_json "$L_CUDA_API" 2>/dev/null | gh_release name "$L_CUDA_ASSET" | awk '{print $2}')"
  fi
  is_sha256 "$want" || want="$(curl -fsSL --retry 3 "$L_CUDA_URL.sha256" 2>/dev/null | awk 'NR==1{print $1}')"
  is_sha256 "$want" || want=""
  if [ -z "$want" ]; then
    if [ "$L_CUDA_CUSTOM" = 1 ]; then
      echo "WARNING: $L_CUDA_URL, the address in the settings, has no checksum: it runs UNVERIFIED."
    else
      echo "No checksum for $L_CUDA_URL: not running it unverified."
      return 1
    fi
  fi
  echo "Downloading llama.cpp for CUDA: $L_CUDA_URL"
  rm -rf "$dir"
  local archive="$ROOT/cuda-download"
  fetch "$L_CUDA_URL" "$archive" "$want" "llama.cpp for CUDA" || return 1
  safe_extract "$archive" "$dir" || { rm -rf "$archive" "$dir"; return 1; }
  rm -f "$archive"
  local bin
  bin="$(find "$dir" -name llama-server -type f | head -1)"
  [ -n "$bin" ] || return 1
  chmod +x "$bin"
  if works "$bin"; then SERVER="$bin"; touch "$dir/.ok"; return 0; fi
  echo "The prebuilt llama-server doesn't run here:"; "$bin" --version 2>&1 | tail -3
  return 1
}

# latest_tag prints the tag of llama.cpp's latest release, which a build
# from source checks out rather than whatever the default branch has.
latest_tag() {
  local tag=""
  latest_release && tag="$(printf '%s' "$LATEST" | gh_release tag)"
  if [ -z "$tag" ]; then
    tag="$(git ls-remote --tags --refs https://github.com/ggml-org/llama.cpp 'refs/tags/b*' 2>/dev/null |
      awk -F/ '{print $3}' | grep -E '^b[0-9]+$' | sort -V | tail -1)"
  fi
  [[ "$tag" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || return 1
  echo "$tag"
}

build_from_source() {
  local dir="$ROOT/src" cuda="$1"
  local out="$ROOT/built-$cuda/llama-server"
  [ -x "$out" ] && works "$out" && { SERVER="$out"; return 0; }
  phase build
  local tag
  tag="$(latest_tag)" || { echo "Couldn't find llama.cpp's latest release."; return 1; }
  echo "Building llama.cpp $tag (CUDA=$cuda). This takes a few minutes."
  command -v cmake >/dev/null || pip install -q cmake
  rm -rf "$dir"
  git -c advice.detachedHead=false clone --depth 1 --branch "$tag" https://github.com/ggml-org/llama.cpp "$dir" || return 1
  local flags=(-DLLAMA_CURL=OFF -DBUILD_SHARED_LIBS=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF -DCMAKE_BUILD_TYPE=Release)
  if [ "$cuda" = 1 ]; then
    local cc
    cc="$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader | head -1 | tr -d . )"
    export PATH="/usr/local/cuda/bin:$PATH"
    flags+=(-DGGML_CUDA=ON "-DCMAKE_CUDA_ARCHITECTURES=${cc:-75}")
  fi
  cmake -S "$dir" -B "$dir/build" "${flags[@]}" >/dev/null || return 1
  cmake --build "$dir/build" --target llama-server -j"$(nproc)" 2>&1 | grep -E '^\[ *[0-9]+%\]' | awk 'NR%10==0' || true
  [ -x "$dir/build/bin/llama-server" ] || return 1
  mkdir -p "$(dirname "$out")" && cp "$dir/build/bin/llama-server" "$out"
  SERVER="$out"
}

get_cpu_prebuilt() {
  local dir="$ROOT/cpu-$ARCH"
  local bin
  bin="$(find "$dir" -name llama-server -type f 2>/dev/null | head -1)"
  [ -f "$dir/.ok" ] && [ -n "$bin" ] && works "$bin" && { SERVER="$bin"; return 0; }
  local want="ubuntu-x64"
  [ "$ARCH" = aarch64 ] && want="ubuntu-arm64"
  latest_release || return 1
  local url="" sum=""
  read -r url sum < <(printf '%s' "$LATEST" | gh_release suffix "-bin-$want.tar.gz" "-bin-$want.zip")
  [ -n "$url" ] || return 1
  if ! is_sha256 "$sum"; then
    echo "llama.cpp's release gives no checksum for $url: not running it unverified."
    return 1
  fi
  echo "Downloading llama.cpp: $url"
  rm -rf "$dir"
  local archive="$ROOT/cpu-download"
  fetch "$url" "$archive" "$sum" "$(basename "$url")" || return 1
  safe_extract "$archive" "$dir" || { rm -rf "$archive" "$dir"; return 1; }
  rm -f "$archive"
  find "$dir" -type f \( -name 'llama-*' -o -name '*.so*' \) -exec chmod +x {} +
  bin="$(find "$dir" -name llama-server -type f | head -1)"
  [ -n "$bin" ] && works "$bin" && { SERVER="$bin"; touch "$dir/.ok"; return 0; }
  return 1
}

phase llama
SERVER=""
if [ "$L_GPU" = 1 ]; then
  get_cuda_prebuilt || build_from_source 1 || die "couldn't get llama.cpp for CUDA"
else
  get_cpu_prebuilt || build_from_source 0 || die "couldn't get llama.cpp"
fi
echo "llama-server: $SERVER"
"$SERVER" --version 2>&1 | grep -i version | head -1

# ------------------------------------------------------------ the model

phase download
export HF_ENDPOINT="$L_HF_ENDPOINT"
if [ -n "$L_HF_TOKEN_FILE" ] && [ -f "$L_HF_TOKEN_FILE" ]; then
  HF_TOKEN_VALUE="$(cat "$L_HF_TOKEN_FILE")"
fi
DIR="$MODELS/${L_REPO//\//__}"
mkdir -p "$DIR"
# hf_curl is curl with the token, if any, in a header that goes through a
# pipe, not on the command line, which others can read.
hf_curl() {
  if [ -n "$HF_TOKEN_VALUE" ]; then
    curl -K <(printf 'header = "Authorization: Bearer %s"\n' "$HF_TOKEN_VALUE") "$@"
  else
    curl "$@"
  fi
}
download() {
  local f="$1" dest="$DIR/$1"
  [ -s "$dest" ] && [ ! -e "$dest.part" ] && { echo "Have $f"; return 0; }
  mkdir -p "$(dirname "$dest")"
  echo "Downloading $f"
  if python3 -c "import huggingface_hub" 2>/dev/null; then
    HF_TOKEN="$HF_TOKEN_VALUE" HF_HUB_DISABLE_PROGRESS_BARS=1 python3 - "$f" <<'PY' && return 0
import os, sys
from huggingface_hub import hf_hub_download
try:
    import hf_transfer  # noqa: F401
    os.environ["HF_HUB_ENABLE_HF_TRANSFER"] = "1"
except ImportError:
    pass
# No token means none, not one huggingface_hub keeps elsewhere.
token = os.environ.pop("HF_TOKEN", "") or False
hf_hub_download(os.environ["L_REPO"], sys.argv[1], local_dir=os.environ["DIR"], token=token)
PY
    echo "huggingface_hub failed; trying curl"
  fi
  touch "$dest.part"
  hf_curl -fL --retry 5 -C - -o "$dest" "$HF_ENDPOINT/$L_REPO/resolve/main/$f" --progress-bar 2>&1 | tr '\r' '\n' | awk 'NR%40==0' || return 1
  rm -f "$dest.part"
}
export DIR
for f in $L_FILES; do download "$f" || die "couldn't download $f"; done
[ -n "$L_MMPROJ" ] && { download "$L_MMPROJ" || L_MMPROJ=""; }
forget_token
unset HF_TOKEN_VALUE L_HF_TOKEN_FILE HF_ENDPOINT

# ------------------------------------------------------------ serve

phase start
kill $BUSY_PID 2>/dev/null
FIRST="$DIR/$(echo $L_FILES | awk '{print $1}')"
ARGS=(-m "$FIRST" --host 127.0.0.1 --port "$L_PORT" -c "$L_CTX" -np "$L_PARALLEL" --alias "$L_ALIAS" --jinja --metrics)
[ -n "$L_NGL" ] && ARGS+=(-ngl "$L_NGL")
[ -n "$L_MMPROJ" ] && ARGS+=(--mmproj "$DIR/$L_MMPROJ")
[ -n "$L_API_KEY" ] && ARGS+=(--api-key "$L_API_KEY")
# shellcheck disable=SC2206
[ -n "$L_EXTRA" ] && ARGS+=($L_EXTRA)
# The command as the log shows it: API keys, which may have spaces, masked
# as whole arguments.
SHOWN=()
hide=0
for a in "${ARGS[@]}"; do
  if [ "$hide" = 1 ]; then SHOWN+=("***"); hide=0; continue; fi
  case "$a" in
    --api-key | --api-key-file) SHOWN+=("$a"); hide=1 ;;
    --api-key=*) SHOWN+=("--api-key=***") ;;
    *) if [ -n "$L_API_KEY" ] && [ "$a" = "$L_API_KEY" ]; then SHOWN+=("***"); else SHOWN+=("$a"); fi ;;
  esac
done
echo "$SERVER ${SHOWN[*]}"
unset L_API_KEY SHOWN
exec "$SERVER" "${ARGS[@]}"
