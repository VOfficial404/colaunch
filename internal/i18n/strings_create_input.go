package i18n

func init() {
	for k, v := range createInputDict {
		dict[k] = v
	}
}

var createInputDict = map[string][3]string{
	"create.input_image":        {"Input image (optional)", "输入图片（可选）", "輸入圖片（選填）"},
	"create.input_first_frame":  {"First frame (optional)", "首帧图片（可选）", "首幀圖片（選填）"},
	"create.input_formats":      {"Static PNG, JPEG, or WebP, up to 20 MiB.", "支持静态 PNG、JPEG 或 WebP，最大 20 MiB。", "支援靜態 PNG、JPEG 或 WebP，最大 20 MiB。"},
	"create.input_upload":       {"When you generate, the image is uploaded to the selected Colab runtime only.", "点击生成后，图片仅上传到当前选中的 Colab 运行时。", "按下生成後，圖片僅上傳到目前選取的 Colab 執行階段。"},
	"create.input_choose":       {"Choose image", "选择图片", "選擇圖片"},
	"create.input_replace":      {"Replace image", "替换图片", "更換圖片"},
	"create.input_remove":       {"Remove image", "移除图片", "移除圖片"},
	"create.input_loading":      {"Opening image…", "正在打开图片…", "正在開啟圖片…"},
	"create.input_picker_err":   {"Couldn't open the file picker. Try again.", "无法打开文件选择器，请重试。", "無法開啟檔案選擇器，請重試。"},
	"create.input_file_err":     {"Couldn't use this image. Choose a readable, static PNG, JPEG, or WebP within 20 MiB and 24 megapixels (16,384 px per side).", "无法使用这张图片。请选择可读取的静态 PNG、JPEG 或 WebP，大小不超过 20 MiB、总像素不超过 2400 万，且每边不超过 16,384 像素。", "無法使用這張圖片。請選擇可讀取的靜態 PNG、JPEG 或 WebP，大小不超過 20 MiB、總像素不超過 2400 萬，且每邊不超過 16,384 像素。"},
	"create.input_denoise":      {"Redraw strength", "重绘强度", "重繪強度"},
	"create.input_denoise_help": {"0.1 keeps more of the original. 1 redraws more.", "0.1 保留更多原图细节，1 重绘更多。", "0.1 保留更多原圖細節，1 重繪更多。"},
	"create.input_again":        {"To remake this result, use the original image in the form and choose Generate.", "要重做此结果，请在创作表单中使用原图，再点击生成。", "若要重做此結果，請在創作表單中使用原圖，再按下生成。"},
	"create.input_denoise_meta": {"Redraw %g", "重绘强度 %g", "重繪強度 %g"},
}
