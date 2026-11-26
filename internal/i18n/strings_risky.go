package i18n

// The strings of the risky features: public links, fast grabbing and web
// tasks on the free tier, off until turned on in the settings.
func init() {
	for k, v := range riskyDict {
		dict[k] = v
	}
}

var riskyDict = map[string][3]string{
	"risky.title": {"Risky features", "高风险功能", "高風險功能"},
	"risky.warning": {
		"These may get runtimes ended or the account restricted by Colab. The consequences are yours.",
		"可能导致运行时被终止或账号受限，后果由你承担。",
		"可能導致執行階段被終止或帳號受限，後果由你自行承擔。",
	},
	"risky.intro": {
		"None of them gets around Colab's rules, but each makes it likelier that Colab ends a runtime or limits the account. They are off until you turn them on, which asks first; turning one off needs nothing.",
		"它们都不是在绕过 Colab 的规则，但都会提高运行时被终止或账号被限制的可能。默认全部关闭，开启时需要确认；关闭无需确认。",
		"它們都不是在規避 Colab 的規則，但都會提高執行階段被終止或帳號被限制的可能。預設全部關閉，開啟時需要確認；關閉無需確認。",
	},
	"risky.faq":     {"Colab FAQ: disallowed activities", "Colab 常见问题：禁止的活动", "Colab 常見問題：禁止的活動"},
	"risky.docs":    {"What these do", "功能说明", "功能說明"},
	"risky.confirm": {"I understand the risks, turn it on", "我了解风险，开启", "我了解風險，開啟"},

	"risky.public_links": {"Public links for web pages and APIs", "允许生成公网分享链接", "允許產生公開分享連結"},
	"risky.public_links_help": {
		"Runtimes get a \"Make a public link\" button for their web, OpenAI and ComfyUI ports: a Cloudflare quick tunnel anyone with the link can reach.",
		"运行时的网页、OpenAI、ComfyUI 端口旁会出现「生成公网链接」：通过 Cloudflare 临时隧道，任何拿到链接的人都能访问。",
		"執行階段的網頁、OpenAI、ComfyUI 連接埠旁會出現「產生公開連結」：透過 Cloudflare 臨時通道，任何拿到連結的人都能存取。",
	},
	"risky.public_links_title": {"Turn on public links?", "开启公网分享链接？", "開啟公開分享連結？"},
	"risky.public_links_text": {
		"Colab's FAQ disallows web service offerings not related to interactive compute on all runtimes, and on the free tier using a runtime mainly through a web interface instead of the notebook. A public link serves a runtime's port to the internet: anyone with it can use it, it takes extra bandwidth, and Colab may end the runtime or restrict the account. See %s",
		"Colab 常见问题禁止所有运行时提供「与交互式计算无关的网络服务」，免费额度下也禁止绕过笔记本界面、主要通过网页界面交互。公网链接会把运行时的端口开放到互联网：任何拿到链接的人都能使用，会额外占用带宽，Colab 可能终止运行时或限制账号。详见 %s",
		"Colab 常見問題禁止所有執行階段提供「與互動式運算無關的網路服務」，免費額度下也禁止繞過筆記本介面、主要透過網頁介面互動。公開連結會把執行階段的連接埠開放到網際網路：任何拿到連結的人都能使用，會額外占用頻寬，Colab 可能終止執行階段或限制帳號。詳見 %s",
	},

	"risky.fast_grab": {"Aggressive grabbing: rounds as often as every 3 seconds", "激进抢卡：最短 3 秒一轮", "激進搶卡：最短 3 秒一輪"},
	"risky.fast_grab_help": {
		"Grabbing waits 10 seconds between rounds at the least; this lowers it to 3.",
		"抢卡每轮间隔默认最少 10 秒，开启后最少 3 秒。",
		"搶卡每輪間隔預設最少 10 秒，開啟後最少 3 秒。",
	},
	"risky.fast_grab_title": {"Turn on aggressive grabbing?", "开启激进抢卡？", "開啟激進搶卡？"},
	"risky.fast_grab_text": {
		"Asking Colab for a runtime every few seconds may get the account rate-limited or flagged for abuse, and Colab may restrict it. See %s",
		"每隔几秒就向 Colab 请求一次运行时，可能导致账号被限流或被标记为滥用，Colab 可能限制该账号。详见 %s",
		"每隔幾秒就向 Colab 請求一次執行階段，可能導致帳號被限流或被標記為濫用，Colab 可能限制該帳號。詳見 %s",
	},

	"risky.free_web_ui": {"Web tasks on the free tier", "免费额度下允许部署网页类任务", "免費額度下允許部署網頁類任務"},
	"risky.free_web_ui_help": {
		"Lets accounts without paid compute units deploy tasks used through a web page, as ComfyUI. Each launch asks first.",
		"允许没有付费计算单元的账号部署通过网页使用的任务（如 ComfyUI），每次部署前都会确认。",
		"允許沒有付費運算單元的帳號部署透過網頁使用的任務（如 ComfyUI），每次部署前都會確認。",
	},
	"risky.free_web_ui_title": {"Allow web tasks on the free tier?", "允许免费额度部署网页类任务？", "允許免費額度部署網頁類任務？"},
	"risky.free_web_ui_text": {
		"On the free tier, Colab's FAQ disallows using a runtime mainly through a web interface instead of the notebook. Colab may end such runtimes at any time or restrict the account. See %s",
		"免费额度下，Colab 常见问题禁止绕过笔记本界面、主要通过网页界面使用运行时。Colab 可能随时终止此类运行时或限制账号。详见 %s",
		"免費額度下，Colab 常見問題禁止繞過筆記本介面、主要透過網頁介面使用執行階段。Colab 可能隨時終止此類執行階段或限制帳號。詳見 %s",
	},

	"share.make":     {"Make a public link", "生成公网链接", "產生公開連結"},
	"share.public":   {"Public on the internet", "公网可访问", "公開網路可存取"},
	"share.badge":    {"Public", "公网", "公開"},
	"share.stop":     {"Close public link", "关闭公网链接", "關閉公開連結"},
	"share.starting": {"Making a public link…", "正在生成公网链接…", "正在產生公開連結…"},
	"share.link":     {"Public link", "公网链接", "公開連結"},
	"share.needs_key": {
		"An API without an API key can't be made public: set one in the deploy form and deploy again.",
		"没有 API 密钥的接口不能公开：请先在部署表单设置 API 密钥并重新部署。",
		"沒有 API 金鑰的介面不能公開：請先在部署表單設定 API 金鑰並重新部署。",
	},
	"share.confirm_title": {"Make %s public?", "公开 %s？", "公開 %s？"},
	"share.confirm_text": {
		"Anyone who gets the link can reach this port, from anywhere. Colab may end the runtime, at once on the free tier, or restrict the account; the traffic takes extra bandwidth. The link lasts until you close it or the runtime stops.",
		"任何拿到链接的人都能从任何地方访问这个端口。Colab 可能终止运行时（免费额度下可能立即终止）或限制账号；流量会额外占用带宽。链接在你关闭它或运行时停止前一直有效。",
		"任何拿到連結的人都能從任何地方存取這個連接埠。Colab 可能終止執行階段（免費額度下可能立即終止）或限制帳號；流量會額外占用頻寬。連結在你關閉它或執行階段停止前一直有效。",
	},
	"share.confirm": {"Make it public", "确认公开", "確認公開"},

	"grab.too_fast": {
		"Requests this frequent may be rate-limited or flagged",
		"请求过于频繁可能被限流或标记",
		"請求過於頻繁可能被限流或標記",
	},

	"freeweb.title": {"Web tasks are off on the free tier", "免费额度下不可部署网页类任务", "免費額度下不可部署網頁類任務"},
	"freeweb.text": {
		"This account has no paid compute units, and this task is used through a web page, which Colab's FAQ disallows on the free tier. You can allow it under Settings → Risky features, at your own risk.",
		"当前账号没有付费计算单元，而此任务需要通过网页使用，Colab 常见问题在免费额度下禁止这种用法。你可以在「设置 → 高风险功能」中自行承担风险开启。",
		"目前帳號沒有付費運算單元，而此任務需要透過網頁使用，Colab 常見問題在免費額度下禁止這種用法。你可以在「設定 → 高風險功能」中自行承擔風險開啟。",
	},
	"freeweb.enable":        {"Turn on in Settings", "在设置中开启", "在設定中開啟"},
	"freeweb.on":            {"Free tier web task: risky", "免费额度网页任务：高风险", "免費額度網頁任務：高風險"},
	"freeweb.confirm_title": {"Launch a web task on the free tier?", "在免费额度下部署网页类任务？", "在免費額度下部署網頁類任務？"},
	"freeweb.confirm_text": {
		"This account has no paid compute units. Using the runtime through a web page goes against what Colab's FAQ allows the free tier: Colab may end it at any time or restrict the account.",
		"当前账号没有付费计算单元。通过网页使用运行时不符合 Colab 常见问题对免费额度的规定：Colab 可能随时终止运行时或限制账号。",
		"目前帳號沒有付費運算單元。透過網頁使用執行階段不符合 Colab 常見問題對免費額度的規定：Colab 可能隨時終止執行階段或限制帳號。",
	},
	"freeweb.confirm": {"Launch anyway", "仍然部署", "仍要部署"},
}
