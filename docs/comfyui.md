# Images and videos with ComfyUI

[English](#deploy-a-model) · [简体中文](#部署模型)

## Deploy a model

1. On **Deploy**, select the built-in **Images and videos (ComfyUI)** task. If a diffusion model was entered in the llama.cpp task, click **Deploy as an image/video model** under the error to carry the repository over.
2. Enter a Hugging Face repository with a single-file diffusion GGUF. For example, `abenzerps/Qwen-Image-2.1-Turbo-Quantized:Q4_K_M` selects that repository's Q4_K_M weights. Leave the quantization on **AUTO** and omit the suffix to let Colaunch choose for the GPU it gets. **Model family → AUTO** detects the family from the name and Hugging Face metadata. You can also select the family manually.
3. Pick a GPU and cost limits, then launch. The task installs the pinned ComfyUI and GGUF extension, downloads the diffusion weights and companion files, and forwards ComfyUI to your device. The built-in companion files are public. A gated model repository still needs Hugging Face access and a token. An optional LoRA must be a `.safetensors` file trained for the selected model.

## Choose a family and GPU

These are Colaunch's estimates in GiB at the built-in generation defaults with Q4_K_M weights. The lower end assumes ComfyUI moves the text encoder off the GPU after reading the prompt. The upper end keeps the weights on the GPU together. Actual usage varies with resolution, number of images or video frames, quantization, and available system RAM.

| Family | Example Hugging Face repository | VRAM estimate |
| --- | --- | ---: |
| Qwen-Image and Qwen-Image-2512 | `city96/Qwen-Image-gguf` | 15.0–23.8 GiB |
| Qwen-Image 2.1 | `unsloth/Qwen-Image-2.1-GGUF` | 6.7–12.6 GiB |
| Qwen-Image 2.1 Turbo | `abenzerps/Qwen-Image-2.1-Turbo-Quantized` | 7.0–12.8 GiB |
| FLUX.1 [dev] / [schnell] | `unsloth/FLUX.1-dev-GGUF`, `unsloth/FLUX.1-schnell-GGUF` | 9.1–13.9 / 9.6–14.4 GiB |
| Z-Image and Z-Image-Turbo | `unsloth/Z-Image-GGUF`, `unsloth/Z-Image-Turbo-GGUF` | 6.1–13.6 GiB |
| Wan 2.1 text-to-video, 1.3B / 14B | `samuelchristlie/Wan2.1-T2V-1.3B-GGUF`, `city96/Wan2.1-T2V-14B-gguf` | 4.2–10.5 / 13.1–19.4 GiB |
| Wan 2.2 TI2V 5B in text-to-video mode | `QuantStack/Wan2.2-TI2V-5B-GGUF` | 8.7–14.9 GiB |

A T4 (16 GB class) is a starting point for Qwen-Image 2.1 or 2.1 Turbo at 1024 × 1024, batch 1, with Q4_K_M. An L4 (24 GB class) leaves more room for higher resolutions and heavier models. If a generation runs out of memory, reduce the size, batch, or video frames, or redeploy with a smaller GGUF or a larger GPU. The built-in workflows cover text-to-image and text-to-video.

Both Qwen-Image 2.1 families use the public Comfy-Org Qwen3-VL 8B W4A8 encoder (6.31 GB on disk) and the 2.1 BF16 VAE. Base defaults to 25 Euler steps. Turbo defaults to eight Euler steps with the official sigma schedule. Both start at 1024 × 1024 and CFG 1, where negative prompts have no effect. Changing Turbo's step count in Create uses ComfyUI's simple scheduler. The checkpoint is tuned for the official eight-step sequence. [Companion files](https://huggingface.co/Comfy-Org/Qwen-Image-2.1/tree/aacf31a6d48e707f352ecba714cee0680da3479b) · [Base template](https://github.com/Comfy-Org/workflow_templates/blob/main/templates/image_qwen_image_2_1_t2i.json) · [Turbo template](https://github.com/Comfy-Org/workflow_templates/blob/main/templates/image_qwen_image_2_1_turbo_t2i.json)

## Open the web workflow or use Create

- **ComfyUI web page.** In **Runtimes**, open the ready runtime and click **Open** beside the ComfyUI endpoint. The forwarded address is normally `http://127.0.0.1:8188`. In ComfyUI's **Workflows** sidebar, open `colaunch-<family>.json`, edit the prompt and queue a run. The loaders already point to the deployed model and companions.
- **Create page.** Click **Create** on the runtime or use **Create** in the sidebar. Pick a running ComfyUI runtime, enter a prompt, adjust the size and generation settings, and click **Generate**. For video, set frames and FPS. You can monitor or stop the current generation, preview results, copy a prompt, or generate again with the same settings. Save or share results you want to keep before quitting Colaunch.

### Use an input image

In **Create**, choose a PNG, JPEG, or WebP up to 20 MiB. Click **Generate** to upload it to the selected Colab runtime. Qwen‑Image, FLUX.1, and Z‑Image use the image as a starting point and center-crop it to the selected aspect ratio. Set **Denoise strength** lower to retain more of the original image or higher for a stronger redraw. Wan 2.2 5B uses the image as the video’s first frame. Wan 2.1 T2V uses a text prompt alone.

## Colab usage rules

Colab may terminate a free managed runtime without warning when you bypass the notebook UI and primarily use a web UI such as ComfyUI for content generation. Google says the free-tier web UI restriction can be lifted with a paid plan and a positive compute-unit balance. Colab prohibits creating deepfakes on all managed runtimes. Get consent before generating images or videos of real people. [Google Colab FAQ](https://research.google.com/colaboratory/faq.html#disallowed-activities)

## 部署模型

1. 在「部署」页选择内置的「生图与生视频（ComfyUI）」任务。如果先在 llama.cpp 任务里填了扩散模型，可点错误下方的「改用生图/视频部署」，直接带入原仓库。
2. 填写含单文件扩散 GGUF 的 Hugging Face 仓库。例如 `abenzerps/Qwen-Image-2.1-Turbo-Quantized:Q4_K_M` 会指定 Q4_K_M。省略后缀并保留量化版本 `AUTO`，应用就会按实际显卡挑选。模型家族也可自动识别或手动选择。
3. 选好 GPU 和费用限制后启动。应用会安装固定版本的 ComfyUI 和 GGUF 扩展，下载模型、文本编码器和 VAE，并转发网页端口。内置配套文件公开可下载。受限的模型仓库需要授权和 Hugging Face 令牌。可选的 LoRA 须为对应模型的 `.safetensors` 文件。

上表给出了支持的文生图、文生视频家族及 Q4_K_M 显存估算。区间下限假设读取提示词后把编码器移出显存，上限估算所有权重同时驻留。Qwen-Image 2.1 Base 和 Turbo 都使用官方重打包的 6.31 GB W4A8 编码器和 BF16 VAE，可从 T4、1024 × 1024、单张图开始试用。L4 有更多空间。显存不足时，可降低分辨率、出图数量、视频帧数或 GGUF 量化体积。Base 默认 25 步，Turbo 默认使用官方 8 步 sigma 序列，两者均使用 Euler 和 CFG 1。CFG 1 时反向提示词不生效。在创作页修改 Turbo 步数会改用 ComfyUI 的 `simple` 调度，建议保留官方默认值。

- 要进入 **ComfyUI 网页**，在「运行时」打开已就绪的运行时，点击 ComfyUI 地址旁的「打开」。默认地址是 `http://127.0.0.1:8188`。从网页侧栏的 **Workflows** 打开 `colaunch-<家族>.json`，修改提示词后排队生成。工作流已选好部署的模型和配套文件。
- 要进入 **创作页**，点运行时卡片上的「去创作」或侧栏「创作」。选择运行时，填写提示词与参数，再点「生成」。生成视频时可选帧数和帧率。生成中可查看进度或停止，生成后可预览、复制提示词或用相同参数再生成。退出应用前请保存或分享需要保留的结果。

在「创作」页可选择最多 20 MiB 的 PNG、JPEG 或 WebP。点击「生成」时，图片才会上传到当前选中的 Colab 运行时。Qwen‑Image、FLUX.1 和 Z‑Image 会以原图为起点，按目标宽高比居中裁切。调低「重绘强度」会保留更多原图，调高则改动更多。Wan 2.2 5B 可用原图作为视频首帧，Wan 2.1 T2V 使用文字提示词。

Colab 免费托管运行时若绕过 Notebook 界面，主要通过 ComfyUI 等网页界面生成内容，可能随时被终止。Google 说明，可通过付费方案并维持正计算单元余额解除这项免费层限制。所有 Colab 托管运行时均禁止制作深度伪造内容。生成涉及真实人物的图片或视频应先征得本人同意。[Google Colab 常见问题](https://research.google.com/colaboratory/faq.html#disallowed-activities)
