# Writing plugins · 编写插件

[English](#english) · [简体中文](#简体中文)

## English

A plugin is what Colaunch runs on a runtime once it's assigned. Deploying a
model is the built-in plugin, written in Go (`internal/plugins/llama.go`).
Yours can be a folder with two files, no Go needed:

```
<plugins folder>/comfyui/
├── plugin.json
└── run.sh
```

The plugins folder is in Colaunch's data folder: **Plugins → Open the
plugins folder** opens it, writing an example plugin there when it's empty.
**Reload plugins** loads what you changed.

### plugin.json

```json
{
  "id": "comfyui",
  "name": {"en": "ComfyUI", "zh-Hans": "ComfyUI", "zh-Hant": "ComfyUI"},
  "description": {"en": "Node-based image generation", "zh-Hans": "节点式图像生成"},
  "script": "run.sh",
  "variant": "VARIANT_GPU",
  "fields": [
    {"key": "checkpoint", "label": {"en": "Checkpoint URL", "zh-Hans": "模型地址"}, "kind": "text", "required": true},
    {"key": "lowvram", "label": "Low VRAM mode", "kind": "switch", "default": "false"},
    {"key": "preview", "label": "Previews", "kind": "select", "options": ["auto", "latent2rgb", "taesd"], "default": "auto"}
  ],
  "ports": [{"name": "ComfyUI", "remote": 8188, "local": 8188, "kind": "web", "path": "/"}],
  "ready": "curl -sf -o /dev/null http://127.0.0.1:8188/",
  "timeout_minutes": 30
}
```

| Field | | |
|---|---|---|
| `id` | the folder's name | lower-case letters, digits, `-` and `_`; a plugin whose ID another has, built in (`llama`) or loaded before, isn't loaded |
| `name`, `description` | | a string, or one per language: `en`, `zh-Hans`, `zh-Hant` |
| `script` | `run.sh` | the bash script that runs on the runtime: a regular file inside the plugin's folder, not an absolute path, nothing with `..`, and not reached through a symbolic link or junction |
| `variant` | | the machine it advises: `VARIANT_CPU`, `VARIANT_GPU`, `VARIANT_TPU` |
| `fields` | | what the Deploy page asks; `kind` is `text`, `password`, `number`, `select` (with `options`) or `switch`; `advanced: true` puts a field under "Advanced" |
| `ports` | | ports of the runtime to forward to `127.0.0.1`; `kind` is `web`, `openai` or `tcp` |
| `ready` | a TCP connect to the first port | a bash command that succeeds once the plugin serves |
| `timeout_minutes` | 30 | how long to wait for `ready` |
| `model` | | for `openai` ports, the model name the in-app chat sends |
| `hf_token` | `false` | `true` gives the script the Hugging Face token of the settings, as `HF_TOKEN`; plugins don't get it otherwise |

A port of kind `openai` serves an OpenAI-compatible API under `/v1`: the
in-app chat talks to it.

### run.sh

The script runs on the runtime with bash, in a folder of its own, in the
background, its output going to the log Colaunch shows. It gets:

| Variable | |
|---|---|
| `COLAUNCH_<KEY>` | each field's value, its key in upper case: `COLAUNCH_CHECKPOINT` |
| `HF_TOKEN` | the Hugging Face token of the settings, only with `"hf_token": true` in plugin.json |
| `COLAUNCH_PLUGIN_DIR` | the plugin's folder on the runtime, also the working directory |
| `COLAUNCH_HOME` | Colaunch's folder on the runtime, `/content/colaunch` |
| `COLAUNCH_BUSY` | a file to touch while working without serving, as while downloading, so the idle watchdog leaves the runtime alone |

Lines `@@PHASE name@@` show `name` as what the plugin is doing. End with
`exec` of the server, so that stopping the plugin stops it:

```bash
# run.sh
set -e
( while true; do touch "$COLAUNCH_BUSY"; sleep 30; done ) & BUSY=$!
echo "@@PHASE install@@"
[ -d ComfyUI ] || git clone --depth 1 https://github.com/comfyanonymous/ComfyUI
pip install -q -r ComfyUI/requirements.txt
echo "@@PHASE download@@"
wget -nc -q -P ComfyUI/models/checkpoints "$COLAUNCH_CHECKPOINT"
kill $BUSY
args=(--listen 127.0.0.1 --port 8188 --preview-method "$COLAUNCH_PREVIEW")
[ "$COLAUNCH_LOWVRAM" = true ] && args+=(--lowvram)
exec python ComfyUI/main.py "${args[@]}"
```

Listen on `127.0.0.1`: the forward reaches it there, and nothing else can.
Don't use port 8080, which is Colab's own Jupyter server.

Values of `password` fields aren't written to `settings.json` or
`state.json`: Colaunch keeps them in the system's keychain, as it does
tokens. The plugin's folder on the runtime, with its script and log, is
readable by its owner alone.

### Plugins in Go

Built-in plugins implement `plugins.Plugin` and call `plugins.Register` in
an `init`. See `internal/plugins/llama.go`. Pull requests of plugins that
many would use are welcome.

---

## 简体中文

插件就是运行时分配好之后在上面运行的东西。部署模型是自带的插件，用 Go 写成
（`internal/plugins/llama.go`）。你自己的插件只需要一个文件夹、两个文件，不用写 Go：

```
<插件文件夹>/comfyui/
├── plugin.json
└── run.sh
```

插件文件夹在 Colaunch 的数据目录里：**插件 → 打开插件文件夹**即可打开（为空时会写入一个示例插件）。
改完后点**重新加载插件**。

`plugin.json` 的字段、`run.sh` 能用到的环境变量，见上方英文部分的两张表，要点：

- 每个表单字段的值以 `COLAUNCH_<KEY 大写>` 环境变量传给脚本；
- 脚本默认拿不到 Hugging Face 令牌，plugin.json 里写 `"hf_token": true` 才会以 `HF_TOKEN` 传入；
- `script` 必须是插件文件夹内的普通文件：不能是绝对路径、不能含 `..`，也不能经由符号链接或目录联接（junction）指到别处；
- `id` 与已有插件（包括内置的 `llama`）重复的插件不会被加载；
- `password` 类型字段的值不会写进 `settings.json` 或 `state.json`，而是和令牌一样存在系统钥匙串里；
- 脚本在运行时上用 bash 后台运行，输出就是 Colaunch 里看到的日志；
- 输出一行 `@@PHASE 名称@@`，界面会显示当前阶段；
- 下载、安装这类不提供服务的耗时操作期间，定期 `touch "$COLAUNCH_BUSY"`，闲置看门狗就不会误停运行时；
- 最后用 `exec` 启动服务，停止插件时才能一并停掉；
- 服务监听 `127.0.0.1`，端口转发能连上，外部访问不到；不要用 8080，那是 Colab 自己的 Jupyter；
- 类型为 `openai` 的端口需要在 `/v1` 下提供 OpenAI 兼容 API，应用内聊天会直接连它。

用 Go 写的内置插件实现 `plugins.Plugin` 接口，并在 `init` 里调用 `plugins.Register`。
欢迎提交大家都用得上的插件。
