# Changelog

The release workflow takes a version's notes from its section here.

## [0.1.1]

- Closing the desktop window keeps deployments, retries and port forwards running. Reopen or quit Colaunch from the tray.
- Android keeps active work in a foreground service. iOS requests short background time and checks runtime connections when the app returns to the foreground.
- Background operation is enabled by default and can be turned off in Settings.

## [0.1.0] - 2026-11-25

The first release.

- One-click deployment of Hugging Face GGUF models with llama.cpp, with an OpenAI-compatible API, llama.cpp's web chat and a chat in the app. The best quantization that fits the GPU you got is picked, KV cache included; multimodal projectors come along.
- CPU, GPU, TPU and High-RAM runtimes, and a least GPU memory: a runtime with less is given back and another assigned.
- Grabbing a runtime: when Colab has no GPU to give, keep asking round after round, falling back to other accelerators in order and, if you like, to your other accounts, with a limit on the wait and a desktop notification once it's got.
- Cost protection enforced on the runtime by a watchdog, with the app closed too: stop on a schedule, when idle, at a spending cap and at a balance floor. Balance, rate and time left, live.
- Several Google accounts, with the balance of each.
- Port forwarding of any port of a runtime to 127.0.0.1, over Colab's own terminal connection.
- Plugins of a `plugin.json` and a bash script.
- English, Simplified Chinese and Traditional Chinese; light and dark themes.
- Windows, macOS, Linux, Android and iOS (sideloaded).
