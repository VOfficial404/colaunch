# Notifications

Colaunch can send a message to your phone or a chat when something happens to a runtime. Set it up in **Settings → Notifications**: add a channel, fill in its values, choose its events, and click **Send test notification**.

## Events

| Event | When |
|---|---|
| A GPU is grabbed (`grabbed`) | Grab mode, or a retry, got a runtime |
| A model is ready (`ready`) | The deploy finished and the ports are forwarded |
| A deploy fails (`failed`) | The plugin or the launch failed |
| A runtime stops (`stopped`) | It stopped, and the notice says why: you stopped it, the schedule did, it was idle, it hit the spending cap or the balance floor, or Colab took it back |
| Grabbing gives up (`grab_gave_up`) | Grab mode reached its longest wait |
| Balance low (`balance_low`) | An account's balance fell below the amount you set under the channel list. You get one notice each time it goes below; it has to go back above that amount before you get another |
| An account is signed out (`signed_out`) | Google stopped accepting the account's sign-in |

Each channel gets each event at most once every 30 seconds. A request that fails is tried once more, and each try gets 15 seconds.

## Channels

### Telegram

1. In Telegram, open **@BotFather** and send `/newbot`. When the bot exists, BotFather sends you its **token**, which looks like `123456789:AA…`.
2. Open a chat with your bot and send it any message. For a group, add the bot to the group and send a message there.
3. Open `https://api.telegram.org/bot<token>/getUpdates` in a browser. The `"chat":{"id": …}` in the reply is the **Chat ID**. Groups' IDs are negative, for example `-100…`. You can also enter a public channel's `@name`.

Colaunch calls `sendMessage` with `{"chat_id", "text"}`.

**Where Telegram is blocked (for example, mainland China):** set **API address** to a reverse proxy of `https://api.telegram.org`. Colaunch then sends to `<API address>/bot<token>/sendMessage`, so the proxy has to pass `/bot…` paths through unchanged. A Cloudflare Worker that forwards every request to `api.telegram.org` is the usual way. The address must be https. Use a proxy you run yourself or trust, because **it sees your bot token**.

### Bark (iOS)

Install Bark from the App Store and open it. The example address on its main screen looks like `https://api.day.app/XXXXXXXXXXXX/…`. The part after the server is your **device key**; copy it.

If you run your own [bark-server](https://github.com/Finb/bark-server), put its address in **Server**. **Group**, **Sound** (for example `minuet`) and **Interruption level** (`active`, `timeSensitive`, `passive`, `critical`) are optional. Colaunch POSTs `{"device_key", "title", "body", "group", …}` to `<server>/push`.

### Server酱 (WeChat)

Sign in at <https://sct.ftqq.com> with WeChat and copy the **SendKey** from the SendKey page. Colaunch POSTs `{"title", "desp"}` to `https://sctapi.ftqq.com/<SendKey>.send`. Server酱³ keys start with `sctp<uid>t`; they go to `https://<uid>.push.ft07.com/send/<SendKey>.send`. A title is one line of at most 32 characters, and longer ones are cut.

### PushPlus (WeChat)

Sign in at <https://www.pushplus.plus> with WeChat and copy your **token** from the one-to-one push page. Colaunch POSTs `{"token", "title", "content", "template": "txt"}` to `https://www.pushplus.plus/send`.

### ntfy

Install the ntfy app and subscribe to a **topic**. Anyone who knows a topic on `ntfy.sh` can read it, so choose a name that is hard to guess. Colaunch keeps the topic as a secret.

For your own server, set **Server**; a server that needs a login takes an access **token**, which is sent as `Authorization: Bearer`. Colaunch publishes JSON (`{"topic", "title", "message"}`) to the server's root URL, so Chinese titles work.

### Discord

In the channel's settings, go to **Integrations → Webhooks → New Webhook → Copy Webhook URL**. Colaunch POSTs `{"content"}` and turns off mentions, so a model's name can't ping anyone.

### Webhook

Colaunch POSTs JSON to any URL:

```json
{"event": "ready", "title": "Colaunch · Ready: Qwen3-8B", "body": "GPU A100 · NVIDIA A100-SXM4-80GB", "time": "2026-05-01T08:30:00Z"}
```

You can add headers, one `Name: value` per line, for example `Authorization: Bearer …`. Any 2xx answer counts as success.

## With the app closed

Normally the app sends notices, so it has to be running, in the foreground or the background. The watchdog on the runtime still enforces your limits when the app is closed. To also hear about those stops, turn on **Also send stop notices from the runtime when the app is closed** for a channel. It is off by default.

When this is on, the app prepares that channel's HTTP request in advance and puts it on the runtime: `/content/colaunch/notices/notices.json`, in a folder only the owner can open (0700) and a file only the owner can read (0600). The request includes the channel's token or key. When the watchdog stops the runtime on schedule, when idle, or at the spending limit, it first sends each request once with Python's `urllib`. Each request gets 10 seconds, redirects are not followed, and a failure doesn't stop the shutdown. The file goes away with the runtime.

If the app is open when the watchdog stops the runtime, a channel with this setting doesn't get a second notice from the app. If Colab takes the runtime back itself, only the app can tell you.

## Security

- Bot tokens, device keys, SendKeys, PushPlus and ntfy tokens, ntfy topics, and Discord and webhook URLs are secrets. Colaunch keeps them in the system keychain (or in `secrets.json`, readable only by you, where there is no keychain), not in `settings.json`. A `settings.json` written with secrets in it is rewritten without them the next time it loads.
- Addresses must be https. http is allowed only for this computer (`localhost`, `127.0.0.1`, `::1`) or a private network (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`), for a Bark or ntfy server of your own. Host names other than `localhost` count as public, because Colaunch can't tell where they lead. Addresses with a user name and password in them are refused; use headers instead.
- Notices never include tokens, API keys or OAuth addresses. Models are named by the repository as parsed (`user/name`), never by the address you typed. Addresses lose their query strings, and anything that looks like a token is replaced with `***`.
- Error messages from failed sends leave out the address, which can contain the token.
