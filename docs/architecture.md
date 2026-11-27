# How Colaunch works

## The parts

| Package | |
|---|---|
| `internal/google` | OAuth 2.0 for installed apps: PKCE, a loopback server on 127.0.0.1, pasting the address on phones; refreshing tokens; finding the Colab extension's client in editors |
| `internal/colab` | Colab's API: runtimes (`colaboratory.googleapis.com/v1beta/runtimes`), compute units (`colab.pa.googleapis.com/v1/user-info`), a runtime's resources; and runtime transport over `/colab/ssh` or `/colab/tty` |
| `internal/agent` | `agent.py`, which runs on the runtime, and the calls to it |
| `internal/tunnel` | Local TCP listeners on 127.0.0.1 with SSH channels or the terminal mux |
| `internal/hf` | a Hugging Face repository's GGUF files, their quantizations, and which fits |
| `internal/plugins` | plugins: the llama one, and those of `plugin.json` |
| `internal/core` | accounts, sessions (a runtime and its plugin), the limits on cost; no interface |
| `internal/chat` | streaming chat with an OpenAI-compatible API |
| `internal/app` | the interface, MyGo's native UI |
| `internal/i18n` | the strings in English, Simplified and Traditional Chinese |

## Launching

1. The plugin checks its values (the llama plugin lists the repository's
   GGUF files) before anything costs money.
2. `CreateRuntime` asks Colab for the machine and waits for the operation.
3. Colaunch connects to the runtime, uploads `agent.py`, and asks
   it what the machine has. With a least GPU memory set, a runtime with
   less is given back and another asked for, up to the number of tries in
   the settings.
4. The watchdog's limits go to the runtime, and the watchdog starts.
5. The plugin deploys. The llama plugin picks the quantization now, for the
   memory the runtime has, and runs `llama.sh` there.
6. The plugin's ports are forwarded to 127.0.0.1.

If the plugin fails, the runtime is given back, unless the settings keep
failed runtimes.

## Runtime transport

Colaunch prefers Colab’s SSH-over-WebSocket endpoint at `/colab/ssh` on the
runtime proxy. The upgrade sends the proxy token, the `colab-cli` client agent
and `ssh-mode-proxy` query values, and an OpenSSH Ed25519 public key in
`X-Colab-Ssh-Pubkey`. Colaunch generates one key per app process and keeps
its private half in memory. The proxy’s TLS certificate authenticates
Colab. Colaunch trusts the SSH host key presented through that proxy on
first use and pins it in memory for the same runtime until the app exits.
The WebSocket carries SSH bytes. Colaunch writes binary frames and accepts
binary and text frames. [Official CLI SSH implementation](https://github.com/googlecolab/google-colab-cli/blob/7370b25f953a3f9a29f925a85607359f2f05d749/src/colab_cli/commands/ssh.py) · [SSH access and status codes](https://github.com/googlecolab/google-colab-cli/blob/7370b25f953a3f9a29f925a85607359f2f05d749/docs/06_ssh_access.md)

One SSH connection as `root` carries independent channels for commands,
stdin uploads, deployment monitoring and `direct-tcpip` to loopback ports.
Commands run without a PTY and return combined output and the exit status.
Uploads check their byte length before atomically renaming a private file
in the destination directory. SSH keepalives detect a dropped connection.
The next operation refreshes the proxy credentials and shares one replacement
connection. Local listeners keep their addresses and new requests redial
through it. A failed remote dial is reported in the session log. Forwarded
traffic updates the watchdog’s activity file. Colab’s SSH connection limit
depends on the plan, up to five. HTTP 429 surfaces the limit without opening
a terminal. [Official CLI SSH access](https://github.com/googlecolab/google-colab-cli/blob/7370b25f953a3f9a29f925a85607359f2f05d749/docs/06_ssh_access.md)

An HTTP 404 or an explicitly unsupported upgrade selects the `/colab/tty`
fallback. Google describes the terminal as a tmux-wrapped bash and removed
its `/colab/tty` transport in favor of `colab ssh`. Multiple attachments can
share a pane, so replay or competing input can disrupt command markers and
foreground processes. Colaunch uses one terminal attachment at a time. The
fallback reads status snapshots or probes the control shell during deployment.
For forwarding it detaches the control shell before opening the agent mux,
and leaves the mux attached while it has clients. Shell-only operations may
need to wait for those clients to disconnect. On reattachment Colaunch asks
a surviving mux to quit before starting bash. The shared terminal can still
replay output or lose markers across a handoff. [Official CLI execution and interactive history](https://github.com/googlecolab/google-colab-cli/blob/7370b25f953a3f9a29f925a85607359f2f05d749/docs/02_execution_and_interactive.md)

The detached installer publishes readiness, liveness and a bounded log tail
through the runtime’s HTTPS Jupyter Contents API. Colaunch validates the
launch identifier and snapshot freshness. If snapshots fail, an SSH exec
channel streams `agent.py wait-ready` with the existing ready marker and
nonce, checksum and offset checks on each reply. The TTY fallback uses a
one-shot probe through its control shell. Log reads try the Contents API
under `/content/colaunch` and then `colaunch/` on HTTP 404, with shell reads
as a fallback. The installer and watchdog keep running during reconnects.

The local TLS wire test covers the SSH upgrade and channels. Whether runtimes
created through `colaboratory.googleapis.com` expose `/colab/ssh` and allow
`direct-tcpip` remains unverified live. The terminal fallback covers runtimes
that report no SSH endpoint.

## The watchdog

`agent.py watchdog` runs detached on the runtime and reads
`/content/colaunch/watchdog.json` every 15 seconds:

- `stop_at`: the time the schedule stops the runtime;
- `budget_at`: when the spending cap or the balance floor would be passed
  at the account's rate, which the app updates while it runs;
- `idle_minutes`: how long without activity: traffic through the tunnel, a
  GPU over 10 % busy, or a plugin touching its busy file.

When one passes, it records why in `stopped.json` and asks Colab to
unassign the runtime, at `http://$TBE_RUNTIME_ADDR/unassign`, as
`google.colab.runtime.unassign()` does. With the app open, Colaunch also
checks the balance every minute and stops the runtime itself.

## Public links

With **Settings → Risky features → Public links** on, a port can be made
public: `agent.py run share-<port>` runs `share.sh` as a plugin, its pid
and log in the plugin's private folder. The script downloads cloudflared's
latest Linux amd64 release, checked against the asset's `digest` in
GitHub's API, and runs `cloudflared tunnel --no-autoupdate --url
http://127.0.0.1:<port>`. The app reads the
`https://….trycloudflare.com` address from the log. Links live only in
memory: stopping the session closes them, and reconnecting after a restart
closes any left running.

## State

`settings.json` and `state.json` (accounts, and sessions with their
runtimes, to reconnect to after a restart) are in the app's data folder.
Refresh tokens and the Hugging Face token are in the system's keychain, or
in `secrets.json`, readable by the user alone, where there is none.
