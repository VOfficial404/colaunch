package i18n

// The strings of checking for updates: the sidebar's notice, the update
// dialog and Settings → About.
func init() {
	for k, v := range updateDict {
		dict[k] = v
	}
}

var updateDict = map[string][3]string{
	"update.available":       {"Version %s is available", "新版本 v%s 可用", "新版本 v%s 可用"},
	"update.available_label": {"Version %s is available: show the update", "新版本 v%s 可用，查看更新", "新版本 v%s 可用，查看更新"},
	"update.title":           {"Update", "更新", "更新"},
	"update.card_title":      {"A new version of Colaunch is out", "Colaunch 有新版本", "Colaunch 有新版本"},
	"update.current":         {"Current version", "当前版本", "目前版本"},
	"update.latest":          {"Latest version", "最新版本", "最新版本"},
	"update.notes":           {"What's new", "更新说明", "更新說明"},
	"update.no_notes":        {"This release has no notes.", "此版本没有更新说明。", "此版本沒有更新說明。"},
	"update.show":            {"View update", "查看更新", "查看更新"},
	"update.install":         {"Update now", "立即更新", "立即更新"},
	"update.download":        {"Download", "下载", "下載"},
	"update.download_apk":    {"Download APK", "下载 APK", "下載 APK"},
	"update.release_page":    {"Open release page", "打开发布页", "開啟發布頁面"},
	"update.later":           {"Later", "稍后", "稍後"},
	"update.skip":            {"Skip this version", "跳过此版本", "略過此版本"},
	"update.skipped":         {"Version %s skipped. You'll hear about the next one.", "已跳过 v%s，下个版本再提醒你。", "已略過 v%s，下個版本再提醒你。"},
	"update.install_help": {
		"Colaunch downloads the update and asks to restart into it.",
		"Colaunch 会下载更新，完成后询问是否重启到新版本。",
		"Colaunch 會下載更新，完成後詢問是否重新啟動到新版本。",
	},
	"update.manual_help": {
		"This copy can't update itself, as one installed by a package manager: download the new version and install it over this one.",
		"当前安装方式无法自动更新（例如通过软件包管理器安装）：请下载新版本覆盖安装。",
		"目前的安裝方式無法自動更新（例如透過套件管理員安裝）：請下載新版本覆蓋安裝。",
	},
	"update.android_help": {
		"The APK downloads in your browser. Open it to install over this version; Android asks you to allow installing apps from unknown sources for the browser the first time.",
		"APK 会在浏览器中下载，打开即可覆盖安装。首次安装时 Android 会要求允许该浏览器「安装未知应用」。",
		"APK 會在瀏覽器中下載，開啟即可覆蓋安裝。首次安裝時 Android 會要求允許該瀏覽器「安裝不明應用程式」。",
	},
	"update.ios_help": {
		"iOS can't install it directly: download the .ipa from the release page and sideload it with AltStore, SideStore, Sideloadly or TrollStore, as you installed this one.",
		"iOS 无法直接安装：请在发布页下载 .ipa，并像安装当前版本一样用 AltStore、SideStore、Sideloadly 或 TrollStore 侧载。",
		"iOS 無法直接安裝：請在發布頁面下載 .ipa，並像安裝目前版本一樣用 AltStore、SideStore、Sideloadly 或 TrollStore 側載。",
	},
	"update.version":       {"Version %s", "版本 %s", "版本 %s"},
	"update.last_check":    {"Last checked %s", "上次检查：%s", "上次檢查：%s"},
	"update.never_checked": {"Not checked for updates yet", "尚未检查过更新", "尚未檢查過更新"},
	"update.checking":      {"Checking for updates…", "正在检查更新…", "正在檢查更新…"},
	"update.auto":          {"Check for updates automatically", "自动检查更新", "自動檢查更新"},
	"update.auto_help": {
		"Shortly after launch, then every 12 hours. Nothing is downloaded until you ask.",
		"启动后不久检查一次，之后每 12 小时一次。只有你确认后才会下载。",
		"啟動後不久檢查一次，之後每 12 小時一次。只有你確認後才會下載。",
	},
	"update.up_to_date":   {"Colaunch is up to date", "已是最新版本", "已是最新版本"},
	"update.up_to_date_v": {"You have version %s, the latest.", "当前版本 %s 是最新的。", "目前版本 %s 是最新的。"},
	"update.found":        {"Version %s is available", "发现新版本 v%s", "發現新版本 v%s"},
	"update.failed":       {"Couldn't check for updates", "检查更新失败", "檢查更新失敗"},
	"update.rate_limited": {"GitHub is limiting requests. Try again after %s.", "GitHub 限制了请求频率，请在 %s 之后重试。", "GitHub 限制了請求頻率，請在 %s 之後重試。"},
	"update.no_release":   {"No release was found.", "未找到发布版本。", "找不到發布版本。"},
	"update.http_status":  {"GitHub answered with error %d.", "GitHub 返回错误 %d。", "GitHub 回傳錯誤 %d。"},
	"update.offline":      {"Couldn't reach GitHub. Check your connection.", "无法连接 GitHub，请检查网络。", "無法連線 GitHub，請檢查網路。"},
	"update.dev_build":    {"This is a development build, which isn't compared with releases.", "这是开发版本，不与正式版比较。", "這是開發版本，不與正式版比較。"},
	"update.just_now":     {"just now", "刚刚", "剛剛"},
	"update.ago":          {"%s ago", "%s前", "%s前"},
}
