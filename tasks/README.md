# Community tasks · 社区任务

[English](#english) · [简体中文](#简体中文)

## English

This folder is Colaunch's community library: **Tasks → Community tasks**
reads `index.json` here, from the `main` branch, and installs the packs it
lists after checking their SHA-256 and showing them for review. See
[docs/plugins.md](../docs/plugins.md) for what a task is.

### Submitting a task

1. Make the task in Colaunch and check it works on a real Colab runtime.
2. **Export → Save as a file** gives `<id>.colaunch.json`. Put it here.
3. Add it to `index.json`:

   ```json
   {
     "id": "my-task",
     "name": {"en": "My task", "zh-Hans": "我的任务"},
     "description": {"en": "What it does, in a sentence."},
     "author": "your GitHub name",
     "path": "my-task.colaunch.json",
     "tags": ["llm"],
     "sha256": "the output of sha256sum my-task.colaunch.json"
   }
   ```

4. Run `go test ./tasks/`: it checks that every checksum matches its file,
   that every pack is valid and that its scripts pass `bash -n`.
5. Open a pull request saying what the task runs and how you tried it.

Change a task's file and its `sha256` together: a pack that doesn't match
its checksum won't install.

### Requirements

- The script installs software from its official source (its own
  installer, PyPI, the project's GitHub repository), at a version that
  doesn't change under users when that's possible.
- Servers listen on `127.0.0.1`, never `0.0.0.0`, and not on port 8080.
- It touches `"$COLAUNCH_BUSY"` while it downloads or installs, and ends
  with `exec` of its server.
- It asks for the Hugging Face token (`hf_token`) only when it needs it,
  as for gated models, and sends it nowhere but Hugging Face.
- No obfuscated code, no `curl | sh` of anything but the project's own
  official installer, nothing downloaded from short links or file hosts.
- A name, a description and a readme that say plainly what it does, what
  it downloads, and, if it can cost free-tier users their runtime, that.

### Review

Maintainers read the whole script. A task is listed when it does what it
says and nothing else, follows the requirements, and is useful to more
than its author. A task can be removed from the index at any time, as
when its source changes or it breaks.

### Not accepted

Anything that breaks
[Colab's rules](https://research.google.com/colaboratory/faq.html#disallowed-activities)
or the law, including:

- file hosting, media serving or streaming, and proxies, VPNs or anything
  that relays others' traffic;
- mining cryptocurrency;
- torrents and other peer-to-peer file sharing;
- deepfakes and generating content of real people without their consent;
- remote desktops and SSH servers for general use, password cracking,
  attacking other systems, or getting around Colab's limits;
- collecting users' data or tokens, or sending them anywhere.

---

## 简体中文

这个目录是 Colaunch 的社区任务库：应用里的 **任务 → 社区任务库** 读取 `main` 分支上的 `index.json`，
安装时校验 sha256 并经过审查页。任务的格式见 [docs/plugins.md](../docs/plugins.md)。

### 投稿方式

1. 在 Colaunch 里做好任务，并在真实的 Colab 运行时上验证可用；
2. **导出 → 保存为文件** 得到 `<id>.colaunch.json`，放进本目录；
3. 在 `index.json` 中添加条目（id、name、description、author、path、tags、sha256，sha256 用 `sha256sum` 计算）；
4. 运行 `go test ./tasks/`，它会检查校验值与文件一致、任务包有效、脚本通过 `bash -n`；
5. 提交 pull request，说明任务运行什么、你如何测试过。

修改任务文件时必须同时更新 `sha256`，否则无法安装。

### 要求

- 从官方来源安装软件（项目自己的安装脚本、PyPI、项目的 GitHub 仓库），尽量固定版本；
- 服务监听 `127.0.0.1`，不要 `0.0.0.0`，不使用 8080 端口；
- 下载、安装期间 touch `"$COLAUNCH_BUSY"`，最后用 `exec` 启动服务；
- 只在确实需要时（如受限模型）申请 Hugging Face 令牌，且只发给 Hugging Face；
- 不得混淆代码，除项目官方安装脚本外不得 `curl | sh`，不得从短链接或网盘下载；
- 名称、描述和说明要写清它做什么、下载什么；可能导致免费额度运行时被终止的，要明确说明。

### 审核标准

维护者会通读整个脚本。任务做的事与描述一致且没有多余行为、符合上述要求、对作者以外的人也有用，才会收录。
来源变化或失效的任务随时可能被移出索引。

### 禁止内容

违反 [Colab 规定](https://research.google.com/colaboratory/faq.html#disallowed-activities)或法律的任何内容，包括：

- 文件托管、媒体服务或推流，代理、VPN 及任何转发他人流量的服务；
- 加密货币挖矿；
- 种子（BT）及其他 P2P 文件分享；
- 深度伪造，以及未经本人同意生成真实人物的内容；
- 通用远程桌面和 SSH 服务、破解密码、攻击其他系统、绕过 Colab 的限制；
- 收集用户数据或令牌，或把它们发往任何地方。
