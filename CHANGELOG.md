# Changelog

The release workflow takes a version's notes from its section here.

## [0.1.2]

Security
- Script plugins only get your Hugging Face token when their plugin.json asks for it with "hf_token": true. The llama plugin keeps the token out of its script and away from llama-server.
- Plugins can only use files inside their own folder, and a plugin can't take another's ID.
- Prebuilt llama.cpp is checked against its SHA-256 before it runs; the source build is pinned to a release tag.
- The Hugging Face token goes only to https://huggingface.co, never to mirrors.
- Password fields, such as API keys, are kept in the system's keychain instead of settings.json and state.json; existing ones are moved there.
- The sign-in can't be cancelled by a stray request, and a pasted address must carry its state.
- Removing a port forward closes its open connections.
- Grabbing only asks on the account you chose: Colab forbids using several accounts to get around its limits.

Performance
- Reading a command's output on the runtime no longer slows down as the output grows (about 5,000× faster for 256 KB).
- Stopping a runtime can no longer freeze the window while its tunnel reconnects.
- Tunnel connections buffer at most 4 MiB each, and support half-closing.
- Fewer requests and redraws, and slower polling while the app is in the background on phones.
- Deploy progress takes one round trip per poll instead of three.

Interface
- First run walks through three steps: OAuth client, Google account, first model.
- The Deploy page has a bar at the bottom with a summary and the Launch button, and warns when there's no spending cap.
- Waiting for a GPU looks different from deploying, with the queue, the round and a countdown.
- Stopping asks the right question for the runtime's state.
- Common errors are explained in your language, with what to do next.
- Darker orange in light mode for readable text, a single column on narrow phones, larger touch targets, and back buttons.
- Free-tier accounts see what Colab restricts on them.

## [0.1.1]

- Closing the desktop window keeps deployments, retries and port forwards running. Reopen or quit Colaunch from the tray.
- Android keeps active work in a foreground service. iOS requests short background time and checks runtime connections when the app returns to the foreground.
- Background operation is enabled by default and can be turned off in Settings.

## [0.1.0] - 2026-11-25

The first release.

- One-click deployment of Hugging Face GGUF models with llama.cpp, with an OpenAI-compatible API, llama.cpp's web chat and a chat in the app. The best quantization that fits the GPU you got is picked, KV cache included; multimodal projectors come along.
- CPU, GPU, TPU and High-RAM runtimes, and a least GPU memory: a runtime with less is given back and another assigned.
- Grabbing a runtime: when Colab has no GPU to give, keep asking round after round, falling back to other accelerators in order, with a limit on the wait and a desktop notification once it's got.
- Cost protection enforced on the runtime by a watchdog, with the app closed too: stop on a schedule, when idle, at a spending cap and at a balance floor. Balance, rate and time left, live.
- Several Google accounts, with the balance of each.
- Port forwarding of any port of a runtime to 127.0.0.1, over Colab's own terminal connection.
- Plugins of a `plugin.json` and a bash script.
- English, Simplified Chinese and Traditional Chinese; light and dark themes.
- Windows, macOS, Linux, Android and iOS (sideloaded).
