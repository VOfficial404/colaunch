#!/usr/bin/env python3
"""Post every family's workflow to a real ComfyUI and check that it passes
ComfyUI's validation: the nodes exist, their inputs have the right names
and types, and the files they load are among those ComfyUI lists.

It isn't run by CI: it needs a ComfyUI checkout with ComfyUI-GGUF in its
custom_nodes, and a Python that runs it (PyTorch for the CPU will do).

    git clone --depth 1 --branch v0.46.0 https://github.com/Comfy-Org/ComfyUI
    git clone https://github.com/leejet/ComfyUI-GGUF ComfyUI/custom_nodes/ComfyUI-GGUF
    git -C ComfyUI/custom_nodes/ComfyUI-GGUF checkout edd981b10e107d3b8f58e16c498f2d08f631bc47
    python -m venv venv
    venv/bin/pip install torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cpu
    venv/bin/pip install -r ComfyUI/requirements.txt -r ComfyUI/custom_nodes/ComfyUI-GGUF/requirements.txt
    python3 scripts/validate_comfy_workflows.py --comfyui ComfyUI --python venv/bin/python

The models are empty files of the same names: running them fails, which
is fine, as validation comes before. The workflows are those of
internal/comfy, written by its TestDumpWorkflows.
"""

import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WIDGET_TYPES = {"INT", "FLOAT", "STRING", "BOOLEAN", "COMBO"}


def request(base, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"null")


def is_widget(spec):
    kind = spec[0]
    # Dynamic combos, as SaveVideo's format, are widgets too.
    return isinstance(kind, list) or kind in WIDGET_TYPES or str(kind).startswith("COMFY_DYNAMICCOMBO")


def check_ui(name, ui, info, widgets):
    """The web page's workflow: its nodes exist and their widgets are in
    the order ComfyUI defines them."""
    problems = []
    for node in ui["nodes"]:
        cls = node["type"]
        if cls not in info:
            problems.append(f"{name}: node {node['id']}: no {cls} in ComfyUI")
            continue
        ours = [w for w in (widgets.get(cls) or []) if w != "$control" and "." not in w]
        order = info[cls].get("input_order", {})
        inputs = {**info[cls]["input"].get("required", {}), **info[cls]["input"].get("optional", {})}
        theirs = [k for k in order.get("required", []) + order.get("optional", []) if is_widget(inputs[k])]
        if theirs[: len(ours)] != ours:
            problems.append(f"{name}: {cls}'s widgets are {theirs}, the workflow has {ours}")
        if len(node["widgets_values"]) != len(widgets.get(cls) or []):
            problems.append(f"{name}: {cls} has {len(node['widgets_values'])} widget values for {widgets[cls]}")
    return problems


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--comfyui", required=True, help="ComfyUI's folder, with custom_nodes/ComfyUI-GGUF")
    ap.add_argument("--python", default=sys.executable, help="the Python that runs ComfyUI")
    ap.add_argument("--go", default="go", help="the go command, to write the workflows")
    ap.add_argument("--port", type=int, default=8199)
    ap.add_argument("--keep", action="store_true", help="keep the temporary folder")
    args = ap.parse_args()

    with socket.socket() as probe:
        if os.name == "nt":
            probe.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
        try:
            probe.bind(("127.0.0.1", args.port))
        except OSError as e:
            ap.error(f"cannot use port {args.port}: {e}")

    tmp = tempfile.mkdtemp(prefix="colaunch-comfy-")
    flows = os.path.join(tmp, "workflows")
    env = dict(os.environ, COLAUNCH_DUMP_WORKFLOWS=flows)
    subprocess.run([args.go, "test", "./internal/comfy", "-run", "^TestDumpWorkflows$", "-count=1"], cwd=REPO, env=env, check=True)
    widgets = json.load(open(os.path.join(flows, "widgets.json"), encoding="utf-8"))
    names = sorted(f[: -len(".api.json")] for f in os.listdir(flows) if f.endswith(".api.json"))

    models = os.path.join(tmp, "models")
    for name in names:
        for f in json.load(open(os.path.join(flows, name + ".files.json"), encoding="utf-8")):
            path = os.path.join(models, f["dir"], *f["name"].split("/"))
            os.makedirs(os.path.dirname(path), exist_ok=True)
            open(path, "wb").close()
    for d in ("user", "output", "input", "temp"):
        os.makedirs(os.path.join(tmp, d), exist_ok=True)

    base = f"http://127.0.0.1:{args.port}"
    log = open(os.path.join(tmp, "comfyui.log"), "w", encoding="utf-8")
    server = subprocess.Popen(
        [args.python, "main.py", "--cpu", "--listen", "127.0.0.1", "--port", str(args.port), "--disable-auto-launch",
         "--models-directory", models, "--user-directory", os.path.join(tmp, "user"),
         "--output-directory", os.path.join(tmp, "output"), "--input-directory", os.path.join(tmp, "input"),
         "--temp-directory", os.path.join(tmp, "temp")],
        cwd=args.comfyui, stdout=log, stderr=subprocess.STDOUT)
    failed = []
    try:
        for _ in range(240):
            if server.poll() is not None:
                sys.exit("ComfyUI stopped; see " + log.name)
            try:
                status, stats = request(base, "/system_stats")
                if status == 200:
                    break
            except (urllib.error.URLError, ConnectionError):
                pass
            time.sleep(1)
        else:
            sys.exit("ComfyUI didn't start; see " + log.name)
        print("ComfyUI", stats["system"].get("comfyui_version"), "Python", stats["system"].get("python_version", "").split()[0])
        _, info = request(base, "/object_info")
        if "UnetLoaderGGUF" not in info:
            sys.exit("ComfyUI-GGUF isn't loaded; see " + log.name)

        for name in names:
            api = json.load(open(os.path.join(flows, name + ".api.json"), encoding="utf-8"))
            if os.sep != "/":
                # ComfyUI lists files in subfolders with the system's
                # separator: on Windows, as this check may run, "\\".
                for node in api.values():
                    for k, v in node["inputs"].items():
                        if k in ("unet_name", "lora_name") and isinstance(v, str):
                            node["inputs"][k] = v.replace("/", os.sep)
            ui = json.load(open(os.path.join(flows, name + ".ui.json"), encoding="utf-8"))
            status, r = request(base, "/prompt", {"prompt": api, "client_id": "colaunch-validate"})
            problems = check_ui(name, ui, info, widgets)
            if status == 200 and not r.get("node_errors"):
                classes = sorted({n["class_type"] for n in api.values()})
                print(f"PASS {name}: {len(api)} nodes ({', '.join(classes)})")
            else:
                failed.append(name)
                print(f"FAIL {name}: HTTP {status}")
                print(json.dumps(r, indent=1, ensure_ascii=False)[:4000])
            for p in problems:
                failed.append(name)
                print("FAIL", p)
            # Running the empty models fails anyway: don't wait for it.
            request(base, "/queue", {"clear": True})
            request(base, "/interrupt", {})
    finally:
        if server.poll() is None:
            if os.name == "nt":
                # The venv launcher starts Python in a child process on Windows.
                subprocess.run(["taskkill", "/pid", str(server.pid), "/t", "/f"], check=True, capture_output=True)
            else:
                server.terminate()
            try:
                server.wait(30)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()
        log.close()
        if args.keep:
            print("Kept", tmp)
        else:
            shutil.rmtree(tmp, ignore_errors=True)
    if failed:
        sys.exit(f"{len(set(failed))} of {len(names)} workflows failed")
    print(f"All {len(names)} workflows pass ComfyUI's validation.")


if __name__ == "__main__":
    main()
