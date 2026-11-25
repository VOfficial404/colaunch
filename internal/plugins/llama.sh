# Colaunch's llama.cpp plugin, run on the runtime by agent.py. The app puts
# its settings in L_* variables above. Lines "@@PHASE name@@" tell the app
# what it's doing.
set -uo pipefail

phase() { echo "@@PHASE $1@@"; }
die() { echo "ERROR: $*"; exit 1; }

# Keep the watchdog from taking downloads and builds for idleness.
( while true; do touch "$COLAUNCH_BUSY"; sleep 30; done ) &
BUSY_PID=$!
trap 'kill $BUSY_PID 2>/dev/null' EXIT

ROOT="$COLAUNCH_HOME/llama"
MODELS="$COLAUNCH_HOME/models"
mkdir -p "$ROOT" "$MODELS"
ARCH="$(uname -m)"
# Where Colab keeps CUDA's libraries, which the prebuilt server links to.
export LD_LIBRARY_PATH="/usr/local/cuda/lib64:/usr/lib64-nvidia:${LD_LIBRARY_PATH:-}"

works() { "$1" --version >/dev/null 2>&1; }

# ------------------------------------------------------------ llama.cpp

get_cuda_prebuilt() {
  local dir="$ROOT/cuda"
  [ -x "$dir/llama-server" ] && works "$dir/llama-server" && { SERVER="$dir/llama-server"; return 0; }
  [ -n "$L_CUDA_URL" ] && [ "$ARCH" = x86_64 ] || return 1
  echo "Downloading llama.cpp for CUDA: $L_CUDA_URL"
  rm -rf "$dir" && mkdir -p "$dir"
  curl -fL --retry 3 -sS "$L_CUDA_URL" | tar -xz -C "$dir" || return 1
  local bin
  bin="$(find "$dir" -name llama-server -type f | head -1)"
  [ -n "$bin" ] || return 1
  chmod +x "$bin"
  if works "$bin"; then SERVER="$bin"; return 0; fi
  echo "The prebuilt llama-server doesn't run here:"; "$bin" --version 2>&1 | tail -3
  return 1
}

build_from_source() {
  local dir="$ROOT/src" cuda="$1"
  local out="$ROOT/built-$cuda/llama-server"
  [ -x "$out" ] && works "$out" && { SERVER="$out"; return 0; }
  phase build
  echo "Building llama.cpp (CUDA=$cuda). This takes a few minutes."
  command -v cmake >/dev/null || pip install -q cmake
  rm -rf "$dir"
  git clone --depth 1 https://github.com/ggml-org/llama.cpp "$dir" || return 1
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
  [ -n "$bin" ] && works "$bin" && { SERVER="$bin"; return 0; }
  local want="ubuntu-x64"
  [ "$ARCH" = aarch64 ] && want="ubuntu-arm64"
  local url
  url="$(curl -fsSL https://api.github.com/repos/ggml-org/llama.cpp/releases/latest | python3 -c "
import json, sys
for a in json.load(sys.stdin)['assets']:
    n = a['name']
    if n.endswith('-bin-$want.zip') or n.endswith('-bin-$want.tar.gz'):
        print(a['browser_download_url']); break
")"
  [ -n "$url" ] || return 1
  echo "Downloading llama.cpp: $url"
  rm -rf "$dir" && mkdir -p "$dir"
  if [[ "$url" == *.zip ]]; then
    curl -fL --retry 3 -sS -o "$dir/l.zip" "$url" && (cd "$dir" && python3 -m zipfile -e l.zip . && rm l.zip) || return 1
  else
    curl -fL --retry 3 -sS "$url" | tar -xz -C "$dir" || return 1
  fi
  find "$dir" -type f \( -name 'llama-*' -o -name '*.so*' \) -exec chmod +x {} +
  bin="$(find "$dir" -name llama-server -type f | head -1)"
  [ -n "$bin" ] && works "$bin" && { SERVER="$bin"; return 0; }
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
[ -n "$L_HF_TOKEN" ] && export HF_TOKEN="$L_HF_TOKEN"
DIR="$MODELS/${L_REPO//\//__}"
mkdir -p "$DIR"
download() {
  local f="$1" dest="$DIR/$1"
  [ -s "$dest" ] && [ ! -e "$dest.part" ] && { echo "Have $f"; return 0; }
  mkdir -p "$(dirname "$dest")"
  echo "Downloading $f"
  if python3 -c "import huggingface_hub" 2>/dev/null; then
    HF_HUB_DISABLE_PROGRESS_BARS=1 python3 - "$f" <<'PY' && return 0
import os, sys
from huggingface_hub import hf_hub_download
try:
    import hf_transfer  # noqa: F401
    os.environ["HF_HUB_ENABLE_HF_TRANSFER"] = "1"
except ImportError:
    pass
hf_hub_download(os.environ["L_REPO"], sys.argv[1], local_dir=os.environ["DIR"])
PY
    echo "huggingface_hub failed; trying curl"
  fi
  local auth=()
  [ -n "${HF_TOKEN:-}" ] && auth=(-H "Authorization: Bearer $HF_TOKEN")
  touch "$dest.part"
  curl -fL --retry 5 -C - "${auth[@]}" -o "$dest" "$HF_ENDPOINT/$L_REPO/resolve/main/$f" --progress-bar 2>&1 | tr '\r' '\n' | awk 'NR%40==0' || return 1
  rm -f "$dest.part"
}
export DIR
for f in $L_FILES; do download "$f" || die "couldn't download $f"; done
[ -n "$L_MMPROJ" ] && { download "$L_MMPROJ" || L_MMPROJ=""; }

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
echo "$SERVER ${ARGS[*]}" | sed "s/--api-key [^ ]*/--api-key ***/"
exec "$SERVER" "${ARGS[@]}"
