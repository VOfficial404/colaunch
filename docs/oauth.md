# The OAuth client · OAuth 客户端

[English](#english) · [简体中文](#简体中文)

## English

Colaunch signs in to Google accounts with OAuth 2.0, and calls Colab with
their tokens. Colab's runtime API is [in beta and on an allowlist](https://docs.google.com/forms/d/e/1FAIpQLSf6q_v7lM7Eqq3PGqMsQBB5pHiuc9XOSsjwJXdT5F2EypmBiA/viewform):
it takes the tokens of OAuth clients Google let in, and the scope
`https://www.googleapis.com/auth/colaboratory` with them. A client you
create in your own Google Cloud project isn't one of them until Google
grants your project access.

So Colaunch needs a client Colab knows. There are three ways to give it one.

### 1. Import the Colab extension's client (desktop)

Google's [Colab extension for VS Code](https://marketplace.visualstudio.com/items?itemName=Google.colab)
is such a client, of the "Desktop app" kind, whose ID and secret ship inside
the extension, as installed apps' do: Google [doesn't treat the secret of
an installed app as a secret](https://developers.google.com/identity/protocols/oauth2/native-app).
Install the extension in VS Code, Cursor, Windsurf or Antigravity, then in
Colaunch press **Accounts → Import from VS Code**. Colaunch reads the client
from the extension's files on your machine and keeps it in its settings.

The consent screen then names the extension's app, as it would if you
signed in from VS Code.

### 2. Your own client, once Google allowed it

If your Google Cloud project got access to the Colab API:

1. In [Google Cloud Console → APIs & Services → Credentials](https://console.cloud.google.com/apis/credentials),
   create an **OAuth client ID** of type **Desktop app**.
2. Enable `colaboratory.googleapis.com` for the project.
3. Paste the client ID and secret into **Settings → OAuth client**.

Colaunch signs in with PKCE and a loopback redirect to `127.0.0.1`, which
Desktop app clients allow without registering redirect URIs.

### 3. A client built in

Builds can come with a client, so their users needn't set one: give it to
`mygo build` with `MYGO_LDFLAGS`:

```sh
MYGO_LDFLAGS="-X colaunch/internal/core.defaultClientID=… -X colaunch/internal/core.defaultClientSecret=…" go tool mygo build
```

The release workflow does so with the repository's secrets
`COLAUNCH_OAUTH_CLIENT_ID` and `COLAUNCH_OAUTH_CLIENT_SECRET`, when they're
set. The repository itself keeps no client.

### Signing in on phones

The browser opens Google's page and sends the answer back to a server
Colaunch runs on `127.0.0.1`. iOS suspends an app in the background, so
that server may not answer: then the browser shows an error page whose
address holds the code. Copy that address and paste it into Colaunch's
sign-in card.

---

## 简体中文

Colaunch 用 OAuth 2.0 登录 Google 账号，再用令牌调用 Colab。Colab 的运行时
接口目前是[测试版、需要白名单](https://docs.google.com/forms/d/e/1FAIpQLSf6q_v7lM7Eqq3PGqMsQBB5pHiuc9XOSsjwJXdT5F2EypmBiA/viewform)：
只接受 Google 放行的 OAuth 客户端，以及 `https://www.googleapis.com/auth/colaboratory`
这个权限范围。你在自己的 Google Cloud 项目里新建的客户端，在 Google 批准前是用不了的。

所以 Colaunch 需要一个 Colab 认可的客户端，有三种方式：

### 1. 导入 Colab 扩展的客户端（桌面）

Google 官方的 [Colab VS Code 扩展](https://marketplace.visualstudio.com/items?itemName=Google.colab)
就是这样的客户端，类型是「桌面应用」，它的 ID 和 secret 跟其他桌面应用一样随扩展一起分发——
Google [并不把桌面应用的 secret 当作机密](https://developers.google.com/identity/protocols/oauth2/native-app)。
在 VS Code、Cursor、Windsurf 或 Antigravity 中装好这个扩展，然后在 Colaunch 里点
**账号 → 从 VS Code 导入**。Colaunch 会从你本机的扩展文件中读取客户端并保存到设置里。

之后登录时的授权页面会显示该扩展的应用名，和你在 VS Code 里登录时一样。

### 2. 自己的客户端（需 Google 批准）

如果你的 Google Cloud 项目已经获准使用 Colab API：

1. 在 [Google Cloud Console → API 和服务 → 凭据](https://console.cloud.google.com/apis/credentials)
   创建类型为**桌面应用**的 **OAuth 客户端 ID**；
2. 为项目启用 `colaboratory.googleapis.com`；
3. 把客户端 ID 和 secret 填到 **设置 → OAuth 客户端**。

Colaunch 使用 PKCE 和回环地址 `127.0.0.1` 回调，桌面应用类型的客户端无需登记回调地址。

### 3. 构建时内置

构建时可以通过 `MYGO_LDFLAGS` 内置客户端，这样用户就不用自己设置（命令见上文英文部分）。
发布工作流会在仓库设置了 `COLAUNCH_OAUTH_CLIENT_ID` 和 `COLAUNCH_OAUTH_CLIENT_SECRET`
这两个 Secrets 时自动内置。仓库本身不包含任何客户端。

### 在手机上登录

浏览器打开 Google 登录页后，会把结果回传给 Colaunch 在 `127.0.0.1` 上开的服务。
iOS 会把后台应用挂起，这个服务可能来不及响应：这时浏览器会显示一个打不开的页面，
它的地址里就带着授权码。把这个地址复制下来，粘贴到 Colaunch 的登录卡片里即可。
