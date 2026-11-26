# Risky features

**Settings → Risky features**, at the bottom of Settings, has three switches. All are off by default, including for settings saved before they existed.

None of them gets around Colab's rules. Each makes it likelier that Colab ends a runtime or restricts the account. **The consequences are yours.** Read what Colab disallows first: [Google Colab FAQ, disallowed activities](https://research.google.com/colaboratory/faq.html#disallowed-activities). Two of its rules apply here:

- On all managed runtimes, Colab disallows "web service offerings not related to interactive compute".
- On the free tier, meaning no positive compute unit balance, it disallows bypassing the notebook UI to interact mainly through a web UI.

Turning a switch on shows what it risks and asks you to confirm with **I understand the risks, turn it on**. Turning it off takes effect at once, with no confirmation.

## Public links

**Public links for web pages and APIs** adds **Make a public link** to each forwarded port of a running runtime that serves a web page (`web`), an OpenAI-compatible API (`openai`) or ComfyUI (`comfyui`). Plain TCP ports can't be made public.

- Each click asks first. Anyone who gets the link can reach the port from anywhere, the traffic takes extra bandwidth, and Colab may end the runtime, at once on the free tier, or restrict the account.
- The link runs through a [Cloudflare quick tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/do-more-with-tunnels/trycloudflare/), which needs no Cloudflare account. On the runtime, Colaunch downloads the latest `cloudflared-linux-amd64` from [cloudflare/cloudflared](https://github.com/cloudflare/cloudflared/releases) and checks it against the SHA-256 that GitHub's release API gives for the asset (its `digest`). If there's no digest, or the file doesn't match, it refuses to run it. It then runs `cloudflared tunnel --no-autoupdate --url http://127.0.0.1:<port>` in the background and reads the `https://….trycloudflare.com` address from its log. cloudflared has 60 seconds from starting to print one.
- **llama.cpp's API** can only be made public if you set an **API key** when you deployed it. Without one, anyone could use it: set a key in the deploy form and deploy again.
- While a link is open, the runtime's card has a red border and a **Public** badge, and the link shows **Public on the internet** next to it.
- Traffic through the public link doesn't count as activity for the idle timeout. A runtime used only through its public link stops once it's idle for that long.
- The link is shown in the app and written to the runtime's log only. Colaunch doesn't put it in notifications, `settings.json` or `state.json`.

**Close it:** click **Close public link**. Links also close when the runtime stops or is given back, and when you turn the setting off. They aren't kept across restarts: when Colaunch reconnects to a runtime after it restarts, it closes any tunnels still running there. Make a new link if you need one.

## Aggressive grabbing

**Aggressive grabbing** lowers the shortest wait between rounds of grabbing from 10 seconds to 3. Without it, any shorter wait is raised to 10 seconds. With an interval under 10 seconds, the grab form shows **Requests this frequent may be rate-limited or flagged**: Colab may limit or flag an account that asks for runtimes every few seconds.

**Turn it off:** turn the switch off. Grabs that start afterwards wait at least 10 seconds between rounds again, and the form resets a shorter interval to 30 seconds.

## Web tasks on the free tier

Colaunch treats a task as a **web task** if it's the built-in ComfyUI task, or if it serves a port of kind `web` or `comfyui`. llama.cpp's OpenAI API isn't a web task.

If the active account has only the free tier (its tier is `NONE`, `FREE` or `UNSPECIFIED`, or it has no compute units left):

- With the switch off, the Deploy page explains why a web task can't be deployed, links to this setting, and disables **Launch**. Colaunch refuses the launch even if it's started some other way.
- With the switch on, each **Launch** asks first, and **Launch anyway** starts the deployment. Colab may end such runtimes at any time or restrict the account.

Accounts with paid compute units aren't affected.

**Turn it off:** turn the switch off. Runtimes that are already running keep running. Stop them on **Runtimes** if you don't want them.
