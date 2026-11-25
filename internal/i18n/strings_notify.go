package i18n

// The strings of notifications: the settings' section, and the notices
// themselves. Merged into dict at init, apart from strings.go.
var notifyStrings = map[string][3]string{
	// Settings
	"notify.title":          {"Notifications", "通知", "通知"},
	"notify.help":           {"Get a message on your phone or in a chat when a GPU is grabbed, a model is ready, or a runtime stops. Tokens and keys are kept in the system's keychain.", "抢到卡、模型就绪或运行时停止时，推送到手机或聊天软件。Token 和密钥保存在系统钥匙串中。", "搶到卡、模型就緒或執行階段停止時，推播到手機或聊天軟體。權杖和金鑰儲存在系統鑰匙圈中。"},
	"notify.none":           {"No channels yet.", "还没有通知渠道。", "尚未新增通知管道。"},
	"notify.add":            {"Add channel", "添加渠道", "新增管道"},
	"notify.edit":           {"Edit", "编辑", "編輯"},
	"notify.delete":         {"Delete", "删除", "刪除"},
	"notify.delete_title":   {"Delete this channel?", "删除这个渠道？", "刪除這個管道？"},
	"notify.delete_text":    {"“%s” will get no more notices, and its token or key is removed from this computer.", "「%s」将不再收到通知，其 Token 或密钥也会从本机删除。", "「%s」將不再收到通知，其權杖或金鑰也會從本機刪除。"},
	"notify.new_title":      {"New channel", "新建渠道", "新增管道"},
	"notify.edit_title":     {"Edit channel", "编辑渠道", "編輯管道"},
	"notify.name":           {"Name", "名称", "名稱"},
	"notify.kind":           {"Type", "类型", "類型"},
	"notify.events":         {"Notify me when", "以下情况通知我", "以下情況通知我"},
	"notify.toggle":         {"Turn “%s” on or off", "开启或关闭「%s」", "開啟或關閉「%s」"},
	"notify.events_summary": {"%d events", "%d 个事件", "%d 個事件"},
	"notify.no_events":      {"No events", "未订阅事件", "未訂閱事件"},
	"notify.all_events":     {"All events", "全部事件", "全部事件"},
	"notify.runtime":        {"Also send stop notices from the runtime when the app is closed", "关掉应用后也由运行时发送停止通知", "關閉應用程式後也由執行階段傳送停止通知"},
	"notify.runtime_help":   {"This channel's request, with its token or key, is put in a private folder on your runtime (readable by its owner alone) and sent when the watchdog stops the runtime on schedule, when idle, or at the spending limit. The runtime is deleted with it.", "会把该渠道的请求（含 Token 或密钥）放到你的运行时上的私有目录（仅所有者可读），在看门狗因定时、闲置或花费限制停机时发送。运行时删除后一并清除。", "會把此管道的請求（含權杖或金鑰）放到你的執行階段上的私人資料夾（僅擁有者可讀取），在看門狗因定時、閒置或花費限制停機時傳送。執行階段刪除後一併清除。"},
	"notify.test":           {"Send test notification", "发送测试通知", "傳送測試通知"},
	"notify.testing":        {"Sending…", "发送中…", "傳送中…"},
	"notify.test_ok":        {"Test notification sent", "测试通知已发送", "測試通知已傳送"},
	"notify.test_failed":    {"Couldn't send the test notification", "测试通知发送失败", "測試通知傳送失敗"},
	"notify.balance":        {"Notify when the balance falls below (compute units)", "余额低于此值时通知（计算单元）", "餘額低於此值時通知（運算單元）"},
	"notify.balance_help":   {"Once each time an account's balance goes under it; 0 turns it off. Channels hear it with “Balance low” checked.", "每次账号余额跌破该值时通知一次；0 为关闭。需在渠道中勾选「余额不足」。", "每次帳號餘額跌破此值時通知一次；0 為關閉。需在管道中勾選「餘額不足」。"},
	"notify.docs":           {"How to set up", "配置说明", "設定說明"},
	"notify.secret_help":    {"Kept in the system's keychain, not in the settings file.", "保存在系统钥匙串中，不写入设置文件。", "儲存在系統鑰匙圈中，不寫入設定檔。"},

	// Kinds of channel
	"notify.kind.telegram":   {"Telegram", "Telegram", "Telegram"},
	"notify.kind.bark":       {"Bark", "Bark", "Bark"},
	"notify.kind.serverchan": {"ServerChan (Server酱)", "Server酱", "Server醬"},
	"notify.kind.pushplus":   {"PushPlus", "PushPlus 推送加", "PushPlus 推送加"},
	"notify.kind.ntfy":       {"ntfy", "ntfy", "ntfy"},
	"notify.kind.discord":    {"Discord", "Discord", "Discord"},
	"notify.kind.webhook":    {"Webhook", "通用 Webhook", "通用 Webhook"},

	"notify.help.telegram":   {"Create a bot with @BotFather for its token, send the bot a message, then find your chat ID in getUpdates. Where Telegram is blocked, set the API address to a reverse proxy of api.telegram.org.", "在 @BotFather 创建机器人获取 Token，先给机器人发一条消息，再在 getUpdates 中找到 Chat ID。国内无法直连时，可把 API 地址改成 api.telegram.org 的反向代理。", "在 @BotFather 建立機器人取得權杖，先傳一則訊息給機器人，再從 getUpdates 找到 Chat ID。無法直連時，可將 API 位址改成 api.telegram.org 的反向代理。"},
	"notify.help.bark":       {"Copy the key from the Bark app on your iPhone: it's the part after the server in the example address. Your own bark-server works too.", "在 iPhone 的 Bark App 中复制 Key：即示例地址中服务器后面的那一段。也支持自建 bark-server。", "在 iPhone 的 Bark App 中複製金鑰：即範例網址中伺服器後面的那一段。也支援自架 bark-server。"},
	"notify.help.serverchan": {"Sign in at sct.ftqq.com with WeChat and copy your SendKey. Server酱³ keys (sctp…) work too.", "在 sct.ftqq.com 用微信登录后复制 SendKey。也支持 Server酱³ 的 sctp 开头的 Key。", "在 sct.ftqq.com 用微信登入後複製 SendKey。也支援 Server醬³ 以 sctp 開頭的金鑰。"},
	"notify.help.pushplus":   {"Sign in at pushplus.plus with WeChat and copy your token.", "在 pushplus.plus 用微信登录后复制 Token。", "在 pushplus.plus 用微信登入後複製權杖。"},
	"notify.help.ntfy":       {"Subscribe to a topic in the ntfy app. On a public server, anyone who knows the topic can read it: pick one hard to guess.", "在 ntfy App 中订阅一个主题。公共服务器上知道主题名的人都能读到，请使用难以猜到的名称。", "在 ntfy App 中訂閱一個主題。公共伺服器上知道主題名稱的人都能讀取，請使用難以猜到的名稱。"},
	"notify.help.discord":    {"In a channel's settings, Integrations → Webhooks → New Webhook, then Copy Webhook URL.", "在 Discord 频道设置中选择「整合」→「Webhook」→「新建 Webhook」，然后复制 Webhook URL。", "在 Discord 頻道設定中選擇「整合」→「Webhook」→「新增 Webhook」，然後複製 Webhook 網址。"},
	"notify.help.webhook":    {"Colaunch POSTs JSON: {\"event\", \"title\", \"body\", \"time\"}.", "Colaunch 会 POST 如下 JSON：{\"event\", \"title\", \"body\", \"time\"}。", "Colaunch 會 POST 如下 JSON：{\"event\", \"title\", \"body\", \"time\"}。"},

	// Their values
	"notify.field.bot_token":  {"Bot token", "Bot Token", "Bot 權杖"},
	"notify.field.chat_id":    {"Chat ID", "Chat ID", "Chat ID"},
	"notify.field.api_base":   {"API address", "API 地址", "API 位址"},
	"notify.field.server":     {"Server", "服务器", "伺服器"},
	"notify.field.device_key": {"Device key", "设备 Key", "裝置金鑰"},
	"notify.field.group":      {"Group", "分组", "群組"},
	"notify.field.sound":      {"Sound", "铃声", "鈴聲"},
	"notify.field.level":      {"Interruption level", "通知级别", "通知等級"},
	"notify.field.sendkey":    {"SendKey", "SendKey", "SendKey"},
	"notify.field.token":      {"Token", "Token", "權杖"},
	"notify.field.topic":      {"Topic", "主题", "主題"},
	"notify.field.url":        {"Webhook URL", "Webhook 地址", "Webhook 網址"},
	"notify.field.headers":    {"Headers, one “Name: value” a line", "请求头，每行一个「名称: 值」", "標頭，每行一個「名稱: 值」"},

	"notify.level.default":        {"Default", "默认", "預設"},
	"notify.level.active":         {"Active", "普通（active）", "一般（active）"},
	"notify.level.time_sensitive": {"Time sensitive", "时效性（timeSensitive）", "時效性（timeSensitive）"},
	"notify.level.passive":        {"Passive", "静默（passive）", "靜默（passive）"},
	"notify.level.critical":       {"Critical", "重要警告（critical）", "重大警示（critical）"},

	// Events
	"notify.event.grabbed":      {"A GPU is grabbed", "抢到卡", "搶到卡"},
	"notify.event.ready":        {"A model is ready", "部署就绪", "部署就緒"},
	"notify.event.failed":       {"A deploy fails", "部署失败", "部署失敗"},
	"notify.event.stopped":      {"A runtime stops", "运行时停止", "執行階段停止"},
	"notify.event.grab_gave_up": {"Grabbing gives up", "抢卡放弃", "搶卡放棄"},
	"notify.event.balance_low":  {"Balance low", "余额不足", "餘額不足"},
	"notify.event.signed_out":   {"An account is signed out", "账号登录失效", "帳號登入失效"},

	// Why a notice didn't go
	"notify.err.kind":         {"Unknown type of channel", "未知的渠道类型", "未知的管道類型"},
	"notify.err.missing":      {"%s is required", "请填写%s", "請填寫%s"},
	"notify.err.bad_value":    {"%s doesn't look right", "%s的格式不对", "%s的格式不正確"},
	"notify.err.bad_url":      {"%s isn't a valid http(s) address", "%s不是有效的 http(s) 地址", "%s不是有效的 http(s) 網址"},
	"notify.err.insecure_url": {"%s must use https; http only for this computer or a private network (10.x, 172.16–31.x, 192.168.x)", "%s必须使用 https；只有本机或内网地址（10.x、172.16–31.x、192.168.x）可以用 http", "%s必須使用 https；只有本機或內網位址（10.x、172.16–31.x、192.168.x）可以用 http"},
	"notify.err.bad_header":   {"Headers must be lines of “Name: value”", "请求头须为每行一个「名称: 值」", "標頭須為每行一個「名稱: 值」"},
	"notify.err.network":      {"Couldn't reach the service", "无法连接到服务", "無法連線到服務"},
	"notify.err.timeout":      {"The service didn't answer in time", "服务响应超时", "服務回應逾時"},
	"notify.err.status":       {"The service answered HTTP %d", "服务返回 HTTP %d", "服務回傳 HTTP %d"},
	"notify.err.rejected":     {"The service refused the notice", "服务拒绝了这条通知", "服務拒絕了這則通知"},

	// The notices
	"notice.grabbed_title":    {"Colaunch · Got a %s", "Colaunch · 抢到 %s", "Colaunch · 搶到 %s"},
	"notice.grabbed_body":     {"%s · %d tries in %s", "%s · 尝试 %d 次，用时 %s", "%s · 嘗試 %d 次，耗時 %s"},
	"notice.ready_title":      {"Colaunch · Ready: %s", "Colaunch · 部署就绪：%s", "Colaunch · 部署就緒：%s"},
	"notice.failed_title":     {"Colaunch · Deploy failed: %s", "Colaunch · 部署失败：%s", "Colaunch · 部署失敗：%s"},
	"notice.gave_up_title":    {"Colaunch · Gave up grabbing: %s", "Colaunch · 已放弃抢卡：%s", "Colaunch · 已放棄搶卡：%s"},
	"notice.stopped_title":    {"Colaunch · Stopped: %s", "Colaunch · 已停止：%s", "Colaunch · 已停止：%s"},
	"notice.stopped_body":     {"%s · ran %s", "%s · 运行了 %s", "%s · 執行了 %s"},
	"notice.stopped_runtime":  {"%s (sent by the runtime)", "%s（由运行时发送）", "%s（由執行階段傳送）"},
	"notice.balance_title":    {"Colaunch · Balance low", "Colaunch · 余额不足", "Colaunch · 餘額不足"},
	"notice.balance_body":     {"%s has %.1f compute units left, below %.1f.", "%s 剩余 %.1f 计算单元，低于 %.1f。", "%s 剩餘 %.1f 運算單元，低於 %.1f。"},
	"notice.signed_out_title": {"Colaunch · Signed out", "Colaunch · 账号登录已失效", "Colaunch · 帳號登入已失效"},
	"notice.signed_out_body":  {"%s needs to sign in again in Colaunch.", "%s 需要在 Colaunch 中重新登录。", "%s 需要在 Colaunch 中重新登入。"},
	"notice.test_title":       {"Colaunch · Test notification", "Colaunch · 测试通知", "Colaunch · 測試通知"},
	"notice.test_body":        {"Notifications from Colaunch reach you here.", "Colaunch 的通知可以送达这里。", "Colaunch 的通知可以送達這裡。"},
}

func init() {
	for k, v := range notifyStrings {
		dict[k] = v
	}
}
