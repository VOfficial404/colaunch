# Tasks · 任务

[English](#english) · [简体中文](#简体中文)

## English

A task is what Colaunch runs on a runtime once it's assigned: a bash
script, the form the Deploy page shows for it, and the ports it serves.
Deploying a model is the built-in task, written in Go
(`internal/plugins/llama.go`). Yours need no Go. In the code and on disk
tasks are still called plugins.

The **Tasks** page lists your tasks and the built-in ones. From there you
can:

- **New task**: write one in the editor;
- **Import**: paste a share code, open a `.colaunch.json` file, or download
  one from an https link;
- **Community tasks**: browse the library of this repository's `tasks/`
  folder;
- on each of your tasks: **Use**, **Edit**, **Export**, **Delete**.

Built-in tasks can't be edited. Tasks you imported are marked **Imported**,
with where they came from.

### The editor

A task has:

| | |
|---|---|
| Name, description | in English, Simplified or Traditional Chinese; one language at least, the others fall back to it |
| ID | lower-case letters, digits, `-` and `_`, made from the name until you change it; it can't be a built-in task's |
| Suggested hardware | CPU, GPU, TPU, or any |
| Hugging Face token | whether the script gets the token of the settings, as `HF_TOKEN` |
| Form fields | key (lower-case letters, digits, `_`), label, type (`text`, `password`, `number`, `select` with options, `switch`), default, required, under Advanced |
| Ports | name, port on the runtime, port on this computer, type (`web`, `openai`, `tcp`), path |
| Ready check | a bash command that succeeds once the task serves; empty waits for the first port |
| Timeout | minutes to wait for the ready check, 30 by default |
| Script | the bash script that runs on the runtime |

**Save** writes the task into the plugins folder and loads it again.

### The plugins folder

Each task is a folder of the plugins folder, in Colaunch's data folder
(**Tasks → Open the tasks folder**):

```
<plugins folder>/comfyui/
├── plugin.json   the manifest
├── run.sh        the script
├── README.md     optional
└── task.json     optional: its author, and where it was imported from
```

You can write these by hand and click **Reload tasks**. `plugin.json`:

```json
{
  "id": "comfyui",
  "name": {"en": "ComfyUI", "zh-Hans": "ComfyUI", "zh-Hant": "ComfyUI"},
  "description": {"en": "Node-based image generation", "zh-Hans": "节点式图像生成"},
  "script": "run.sh",
  "variant": "VARIANT_GPU",
  "fields": [
    {"key": "checkpoint", "label": {"en": "Checkpoint URL", "zh-Hans": "模型地址"}, "kind": "text"},
    {"key": "lowvram", "label": "Low VRAM mode", "kind": "switch", "default": "false"},
    {"key": "preview", "label": "Previews", "kind": "select", "options": ["auto", "latent2rgb", "taesd"], "default": "auto"}
  ],
  "ports": [{"name": "ComfyUI", "remote": 8188, "local": 8188, "kind": "web", "path": "/"}],
  "ready": "curl -sf -o /dev/null http://127.0.0.1:8188/",
  "timeout_minutes": 30
}
```

| Field | Default | |
|---|---|---|
| `id` | the folder's name | lower-case letters, digits, `-` and `_`; a task whose ID another has, built in (`llama`) or loaded before, isn't loaded |
| `name`, `description` | | a string, or one per language: `en`, `zh-Hans`, `zh-Hant` |
| `script` | `run.sh` | a regular file inside the task's folder: not an absolute path, nothing with `..`, not reached through a symbolic link or junction |
| `variant` | | the machine it advises: `VARIANT_CPU`, `VARIANT_GPU`, `VARIANT_TPU` |
| `fields` | | what the Deploy page asks; `advanced: true` puts a field under "Advanced" |
| `ports` | | ports of the runtime to forward to `127.0.0.1`, from 1 to 65535, not 8080 |
| `ready` | a TCP connect to the first port | |
| `timeout_minutes` | 30 | |
| `model` | | for `openai` ports, the model name the in-app chat sends |
| `model_field` | | the key of the field whose value is that model name, as the model to pull |
| `hf_token` | `false` | `true` gives the script the Hugging Face token as `HF_TOKEN`; tasks don't get it otherwise |

A port of kind `openai` serves an OpenAI-compatible API under `/v1`: the
in-app chat talks to it.

### The script

The script runs on the runtime with bash, as root, in a folder of its own,
in the background, its output going to the log Colaunch shows. It gets:

| Variable | |
|---|---|
| `COLAUNCH_<KEY>` | each field's value, its key in upper case: `COLAUNCH_CHECKPOINT` |
| `HF_TOKEN` | only with `"hf_token": true` |
| `COLAUNCH_PLUGIN_DIR` | the task's folder on the runtime, also the working directory |
| `COLAUNCH_HOME` | Colaunch's folder on the runtime, `/content/colaunch` |
| `COLAUNCH_BUSY` | a file to touch while working without serving, as while downloading, so the idle watchdog leaves the runtime alone |

Lines `@@PHASE name@@` show `name` as what the task is doing. Listen on
`127.0.0.1`: the forward reaches it there, and nothing else can. Don't use
port 8080, Colab's own Jupyter server. End with `exec` of the server, so
that stopping the task stops it:

```bash
set -euo pipefail
( while true; do touch "$COLAUNCH_BUSY"; sleep 30; done ) &
BUSY=$!
echo "@@PHASE install@@"
[ -d ComfyUI/.git ] || git clone --depth 1 https://github.com/comfyanonymous/ComfyUI.git
python3 -m pip install -q -r ComfyUI/requirements.txt
kill "$BUSY"
cd ComfyUI
exec python3 main.py --listen 127.0.0.1 --port 8188
```

Values of `password` fields aren't written to `settings.json` or
`state.json`: Colaunch keeps them in the system's keychain.

### Task packs and share codes

**Export** gives a task as one file, `<id>.colaunch.json`:

```json
{
  "format": "colaunch-task",
  "version": 1,
  "manifest": { "…": "the fields of plugin.json" },
  "script": "#!/bin/bash …",
  "readme": "optional Markdown",
  "author": "optional"
}
```

or as a share code: `colaunch-task:` followed by the file, gzipped, in
base64url without padding. Exports hold the script, the form and its
defaults, never what you typed in the form or your tokens.

A pack is at most 256 KB, unzipped: larger files and codes that unzip to
more are refused. Its manifest is checked as the editor checks it (the ID,
field keys and types, ports from 1 to 65535 and not 8080, a script that
isn't empty), and installing it writes a folder that is then loaded with all
the checks of the plugins folder. The pack's `manifest.script` is ignored:
the script is always written as `run.sh`.

Links can import too:

- `https://…` links to a `.colaunch.json`, at most 256 KB, within 20
  seconds, redirected only to https. GitHub file pages
  (`github.com/owner/repo/blob/branch/path`) are read from
  `raw.githubusercontent.com`.
- `colaunch://import?url=https://…` and `colaunch://import?code=colaunch-task:…`
  open Colaunch on the task.

### Review

Whatever a task comes from, it shows in full before it installs, and that
can't be skipped: its name, author, description and source; what it will
do (run its script on the runtime, the ports it forwards, whether it gets
the Hugging Face token, the machine it advises, what its form asks, its
ready check); and the whole script, to read and copy. **I've read the
script, install** installs it; nothing runs until you launch it from
Deploy.

A task whose ID a built-in task has must be installed under another ID.
One whose ID a task of yours has replaces it only when you choose
**Replace my task**, or installs under another ID.

### The community library

**Community tasks** reads
<https://raw.githubusercontent.com/VOfficial404/colaunch/main/tasks/index.json>.
Installing one downloads its `.colaunch.json`, checks its SHA-256 against
the index, which the review of its pull request approved, and shows the
review. To add one, see [tasks/README.md](../tasks/README.md).

### Tasks in Go

Built-in tasks implement `plugins.Plugin` and call `plugins.Register` in an
`init`. See `internal/plugins/llama.go`.

---

## 简体中文

任务就是运行时分配好之后在上面运行的东西：一段 bash 脚本、部署页上为它显示的表单，以及它提供服务的端口。
部署模型是自带的任务，用 Go 写成（`internal/plugins/llama.go`）；你自己的任务不需要写 Go。代码和磁盘上任务仍叫 plugin（插件）。

**任务**页列出你的任务和内置任务，可以：

- **新建任务**：在编辑器里编写；
- **导入**：粘贴分享码、打开 `.colaunch.json` 文件，或从 https 链接下载；
- **社区任务库**：浏览本仓库 `tasks/` 目录里的任务；
- 对你自己的任务：**使用**、**编辑**、**导出**、**删除**（删除前会确认）。

内置任务不能编辑。导入的任务会标记为**导入**并显示来源。

### 编辑器

名称和描述（简体、繁体、英文，至少一种）、ID（根据名称自动生成，可修改，不能与内置任务重复）、建议硬件、是否需要
Hugging Face 令牌、表单字段（键名、标签、类型 text/password/number/select/switch、默认值、选项、必填、高级）、
端口（名称、运行时端口、本机端口、类型 web/openai/tcp、路径）、就绪检查命令、超时和脚本。**保存**即写入插件文件夹并重新加载。

插件文件夹里每个任务是一个文件夹（`plugin.json`、`run.sh`，可选 `README.md` 和记录作者与来源的 `task.json`），
也可以手写后点**重新加载任务**。字段和环境变量见英文部分的表格，要点：

- 每个表单字段的值以 `COLAUNCH_<KEY 大写>` 环境变量传给脚本；
- 脚本默认拿不到 Hugging Face 令牌，`"hf_token": true` 才会以 `HF_TOKEN` 传入；
- `script` 必须是任务文件夹内的普通文件：不能是绝对路径、不能含 `..`、不能经由符号链接或目录联接指到别处；
- 端口范围 1–65535，不能用 8080（Colab 自己的 Jupyter）；服务监听 `127.0.0.1`；
- 下载、安装期间定期 `touch "$COLAUNCH_BUSY"`，闲置看门狗就不会误停运行时；
- 输出 `@@PHASE 名称@@` 显示当前阶段；最后用 `exec` 启动服务；
- `openai` 类型端口需在 `/v1` 提供 OpenAI 兼容 API；`model_field` 指定哪个字段的值作为应用内聊天发送的模型名；
- `password` 字段的值存在系统钥匙串里，不写进 `settings.json`。

### 任务包与分享码

**导出**得到单个文件 `<id>.colaunch.json`：
`{"format":"colaunch-task","version":1,"manifest":{…plugin.json 的字段…},"script":"…","readme":"可选","author":"可选"}`；
或分享码：`colaunch-task:` 加上 gzip 压缩后的 JSON 的 base64url（无填充）。导出内容不包含你填写的值和令牌。

任务包解压后最多 256 KB，更大的文件、解压后超限的分享码（gzip 炸弹）都会被拒绝；清单按编辑器的规则校验，
安装时写成文件夹后再经过插件文件夹的全部安全检查。包里的 `manifest.script` 会被忽略，脚本总是写成 `run.sh`。

也可以用链接导入：`https://` 链接（最多 256 KB、20 秒超时、只跟随到 https 的重定向，GitHub 文件页面会转为
`raw.githubusercontent.com`），以及 `colaunch://import?url=https://…`、`colaunch://import?code=colaunch-task:…` 深链接。

### 审查

无论来源，安装前都会完整显示任务，且不可跳过：名称、作者、描述、来源；它会做什么（在运行时执行脚本、转发哪些端口、
是否拿到 Hugging Face 令牌、建议硬件、表单内容、就绪检查）；以及完整脚本（等宽、可滚动、可复制）。
点**我已查看脚本，安装**才会安装；安装后不会自动运行。ID 与内置任务重复时必须换一个 ID；与你自己的任务重复时，
需选择**覆盖我的任务**或换一个 ID。

### 社区任务库

**社区任务库**读取 `tasks/index.json`，安装时下载对应的 `.colaunch.json`，校验它与索引中 sha256 一致后再进入审查。
投稿方式见 [tasks/README.md](../tasks/README.md)。
