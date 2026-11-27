package i18n

func init() {
	dict["forward.checking"] = [3]string{"Checking local ComfyUI access…", "正在检查本机能否访问 ComfyUI…", "正在檢查本機能否存取 ComfyUI…"}
	dict["forward.verified"] = [3]string{"Local ComfyUI access verified", "本机已验证可访问 ComfyUI", "本機已驗證可存取 ComfyUI"}
	dict["forward.unavailable"] = [3]string{"Local ComfyUI access unavailable", "本机无法访问 ComfyUI", "本機無法存取 ComfyUI"}
	dict["forward.retry"] = [3]string{"Retry forward check", "重新检查转发", "重新檢查轉送"}
	dict["forward.ssh_prohibited"] = [3]string{"The SSH server refused direct-tcpip forwarding", "SSH 服务拒绝了 direct-tcpip 端口转发", "SSH 伺服器拒絕了 direct-tcpip 連接埠轉送"}
	dict["forward.ssh_disconnected"] = [3]string{"SSH disconnected. Retry to reconnect and check this port.", "SSH 已断开。重试将重新连接并检查此端口。", "SSH 已中斷。重試將重新連線並檢查此連接埠。"}
	dict["forward.local_busy"] = [3]string{"Local port %d could not be opened. Trying another port.", "无法打开本机端口 %d，正在尝试其他端口。", "無法開啟本機連接埠 %d，正在嘗試其他連接埠。"}
	dict["forward.listening"] = [3]string{"Listening on %s for runtime port %d (%s).", "本机 %s 正在监听，对应运行时端口 %d（%s）。", "本機 %s 正在監聽，對應執行階段連接埠 %d（%s）。"}
	dict["forward.check_failed"] = [3]string{"ComfyUI forward at %s failed its HTTP check. %s", "%s 的 ComfyUI 转发未通过 HTTP 检查。%s", "%s 的 ComfyUI 轉送未通過 HTTP 檢查。%s"}
	dict["forward.check_ready"] = [3]string{"ComfyUI answered /system_stats through %s.", "已通过 %s 收到 ComfyUI 的 /system_stats 响应。", "已透過 %s 收到 ComfyUI 的 /system_stats 回應。"}
}
