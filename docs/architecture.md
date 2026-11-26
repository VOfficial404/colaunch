# How Colaunch works

## The parts

| Package | |
|---|---|
| `internal/google` | OAuth 2.0 for installed apps: PKCE, a loopback server on 127.0.0.1, pasting the address on phones; refreshing tokens; finding the Colab extension's client in editors |
| `internal/colab` | Colab's API: runtimes (`colaboratory.googleapis.com/v1beta/runtimes`), compute units (`colab.pa.googleapis.com/v1/user-info`), a runtime's resources; and its terminal, `/colab/tty`, as a shell and as a raw stream |
| `internal/agent` | `agent.py`, which runs on the runtime, and the calls to it |
| `internal/tunnel` | TCP over the agent's mux: listeners on 127.0.0.1 |
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
3. Colaunch opens a terminal on the runtime, uploads `agent.py`, and asks
   it what the machine has. With a least GPU memory set, a runtime with
   less is given back and another asked for, up to the number of tries in
   the settings.
4. The watchdog's limits go to the runtime, and the watchdog starts.
5. The plugin deploys. The llama plugin picks the quantization now, for the
   memory the runtime has, and runs `llama.sh` there.
6. The plugin's ports are forwarded to 127.0.0.1.

If the plugin fails, the runtime is given back, unless the settings keep
failed runtimes.

## The terminal as a transport

Colab's proxy serves the runtime's Jupyter server and a terminal, both
behind its token. Colaunch runs deployment commands over the terminal:

- **Commands** run in a bash without readline, whose terminal echoes
  nothing. Each prints markers around its output, typed with an empty pair
  of quotes inside (`@@S""TART-1@@`) so that the terminal echoing them, if
  it does, doesn't print what is looked for. A new terminal is ready once
  it prints one marker of a random nonce of its own: what it printed
  before, as an earlier terminal's output replayed, is dropped.
- **Files** go up by switching the terminal to raw input, which has no
  limit on the length of lines, and having `head -c` read exactly the
  base64 sent.
- **The tunnel** runs `agent.py mux` in a terminal of its own, raw: lines
  of `O id port`, `D id base64`, `C id` carry many TCP connections both
  ways.
- **Waiting for a plugin** runs `agent.py wait-ready <id>` in a terminal of
  its own, raw, as the tunnel does. Each request, `W nonce offset
  base64(check)`, gets one line, `@@WR nonce crc32 payload@@`: `ready`, or
  the plugin's liveness and a chunk of its log from offset. A line without
  the request's nonce is ignored, one with a bad checksum or range is asked
  again; a terminal that closes, doesn't answer or keeps answering badly is
  replaced by another on the same runtime, reading on from the last offset
  confirmed. The installer keeps running during reconnects. If the monitor
  cannot connect within 15 seconds, Colaunch tries a read-only probe over
  the existing control terminal. Each poll pipes one request to
  `agent.py wait-ready`, which answers and exits. Colaunch can recover a
  complete reply after a lost START or END marker by checking the nonce,
  checksum, and log range. A stream's unique ready marker can follow
  leftover output on the same line. The quoted marker in the echoed
  command ends with a quote and cannot complete the handshake.

- **Reading logs** can use the runtime's HTTPS Jupyter Contents API.
  Colaunch requests only the plugin's log path and falls back to the shell
  for manual log reads if the API cannot serve the file. After a monitoring
  failure, it gives the file API up to three seconds to recover the last
  24 lines before applying the normal runtime release policy. This depends
  on the runtime exposing `/content` as the Contents root.

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
