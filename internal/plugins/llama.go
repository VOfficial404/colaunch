package plugins

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/comfy"
	"colaunch/internal/hf"
)

//go:embed llama.sh
var llamaScript string

// LlamaPort is where llama-server listens on the runtime: not 8080, which
// is Colab's own Jupyter server.
const LlamaPort = 8765

// DefaultCUDAURL is llama-server built for CUDA by this repository's
// workflow (.github/workflows/llama-cuda.yml), which saves building it on
// the runtime. It's checked against the sha256 GitHub's API gives for the
// release's asset, or else the .sha256 the workflow publishes beside it.
var DefaultCUDAURL = "https://github.com/VOfficial404/colaunch/releases/download/llama-cuda/llama-cuda-linux-x64.tar.gz"

// releaseAsset finds the GitHub release an asset's download address is
// of: the release in GitHub's API, and the asset's name. ok is false for
// an address elsewhere.
func releaseAsset(address string) (api, name string, ok bool) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" {
		return "", "", false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", "", false
		}
	}
	api = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s",
		url.PathEscape(parts[0]), url.PathEscape(parts[1]), url.PathEscape(parts[4]))
	return api, parts[5], true
}

// cudaSource is where llama.cpp for CUDA comes from: the settings', when
// set, else DefaultCUDAURL. custom tells the script that it may run it
// unverified, when there's no checksum to check it against.
func cudaSource(settings map[string]string) (address, api, asset string, custom bool) {
	address = strings.TrimSpace(settings["llama_cuda_url"])
	custom = address != "" && address != DefaultCUDAURL
	if address == "" {
		address = DefaultCUDAURL
	}
	api, asset, _ = releaseAsset(address)
	return address, api, asset, custom
}

// Llama deploys a GGUF model from Hugging Face with llama.cpp's server,
// which has an OpenAI-compatible API and a web page to chat on.
type Llama struct{}

func init() {
	if err := Register(Llama{}); err != nil {
		panic(err)
	}
}

func (Llama) ID() string { return "llama" }

func (Llama) Name() Text {
	return Text{"en": "Deploy a model", "zh-Hans": "部署模型", "zh-Hant": "部署模型"}
}

func (Llama) Description() Text {
	return Text{
		"en":      "Runs a GGUF model from Hugging Face with llama.cpp, picking the best quantization that fits the GPU you got, and serves an OpenAI-compatible API and a chat page.",
		"zh-Hans": "用 llama.cpp 运行 Hugging Face 上的 GGUF 模型，按分到的显卡自动挑选放得下的最佳量化版本，提供 OpenAI 兼容 API 和网页聊天。",
		"zh-Hant": "用 llama.cpp 執行 Hugging Face 上的 GGUF 模型，依分配到的顯示卡自動挑選放得下的最佳量化版本，提供 OpenAI 相容 API 與網頁聊天。",
	}
}

// QuantOptions are the choices of quantization; "AUTO" picks for the GPU.
var QuantOptions = []string{"AUTO", "Q8_0", "Q6_K", "Q5_K_M", "Q4_K_M", "Q4_K_XL", "IQ4_XS", "Q3_K_M", "Q2_K", "BF16", "F16"}

func (Llama) Fields() []Field {
	return []Field{
		{Key: "repo", Kind: FieldText, Required: true, Placeholder: "unsloth/Qwen3-8B-GGUF",
			Label: Text{"en": "Hugging Face GGUF repository", "zh-Hans": "Hugging Face GGUF 仓库", "zh-Hant": "Hugging Face GGUF 儲存庫"},
			Help:  Text{"en": "user/name, or its address. Add :Q4_K_M to choose a quantization.", "zh-Hans": "填 用户/仓库 或网址，末尾加 :Q4_K_M 可指定量化。", "zh-Hant": "填 使用者/儲存庫 或網址，末尾加 :Q4_K_M 可指定量化。"}},
		{Key: "quant", Kind: FieldSelect, Default: "AUTO", Options: QuantOptions,
			Label: Text{"en": "Quantization", "zh-Hans": "量化版本", "zh-Hant": "量化版本"},
			Help:  Text{"en": "AUTO picks the best one that fits the machine you get.", "zh-Hans": "AUTO 会按实际分到的机器挑放得下的最佳版本。", "zh-Hant": "AUTO 會依實際分配到的機器挑放得下的最佳版本。"}},
		{Key: "ctx", Kind: FieldNumber, Default: "8192",
			Label: Text{"en": "Context length", "zh-Hans": "上下文长度", "zh-Hant": "上下文長度"}},
		{Key: "parallel", Kind: FieldNumber, Default: "1", Advanced: true,
			Label: Text{"en": "Parallel requests", "zh-Hans": "并发请求数", "zh-Hant": "並行請求數"}},
		{Key: "api_key", Kind: FieldPassword, Advanced: true,
			Label: Text{"en": "API key (optional)", "zh-Hans": "API 密钥（可选）", "zh-Hant": "API 金鑰（選填）"}},
		{Key: "local_port", Kind: FieldNumber, Default: "8080", Advanced: true,
			Label: Text{"en": "Local port", "zh-Hans": "本机端口", "zh-Hant": "本機連接埠"}},
		{Key: "args", Kind: FieldText, Placeholder: "--temp 0.7", Advanced: true,
			Label: Text{"en": "More llama-server arguments", "zh-Hans": "额外 llama-server 参数", "zh-Hant": "額外 llama-server 參數"}},
	}
}

func (l Llama) Validate(ctx context.Context, values map[string]string, hfToken string) (*Hardware, error) {
	ref, err := hf.ParseRef(values["repo"])
	if err != nil {
		return nil, err
	}
	files, err := hf.ListFiles(ctx, ref.Repo, hfToken)
	if err != nil {
		return nil, err
	}
	// Image and video models come as GGUF too, for ComfyUI: refuse them
	// now, before a GPU is assigned for nothing. A mirror that doesn't
	// say what the model is lets it through.
	if info, err := hf.FetchInfo(ctx, ref.Repo, hfToken); err == nil {
		if err := info.CheckServable(); err != nil {
			return nil, notLLM(ref.Repo, info, err)
		}
	}
	quants := hf.Quants(files)
	if len(quants) == 0 {
		return nil, hf.NoQuants(ref.Repo, files)
	}
	want := wantQuant(ref, values)
	smallest := quants[0]
	for _, q := range quants {
		if q.Size < smallest.Size {
			smallest = q
		}
		if want != "" && q.Name == want {
			smallest = q
			break
		}
	}
	if want != "" && smallest.Name != want {
		return nil, fmt.Errorf("%s has no %s file", ref.Repo, want)
	}
	return &Hardware{Variant: colab.GPU, MinMemory: hf.Need(smallest.Size, atoi(values["ctx"], 8192))}, nil
}

func wantQuant(ref hf.Ref, values map[string]string) string {
	if ref.Quant != "" {
		return ref.Quant
	}
	if q := strings.ToUpper(values["quant"]); q != "AUTO" {
		return q
	}
	return ""
}

func (l Llama) Deploy(ctx context.Context, env *Env) (*Result, error) {
	ref, err := hf.ParseRef(env.Values["repo"])
	if err != nil {
		return nil, err
	}
	env.Phase("resolve")
	files, err := hf.ListFiles(ctx, ref.Repo, env.HFToken)
	if err != nil {
		return nil, err
	}
	quants := hf.Quants(files)
	if len(quants) == 0 {
		return nil, hf.NoQuants(ref.Repo, files)
	}
	ctxLen := atoi(env.Values["ctx"], 8192)

	// The memory the model may take: the GPU's, or else the machine's.
	gpuMem := env.Status.VRAMMB() << 20
	ram := env.Status.Memory.Available
	mem, spill := gpuMem, ram*6/10
	if gpuMem == 0 {
		mem, spill = ram*85/100, 0
	}
	choice, err := hf.Pick(quants, mem, spill, ctxLen, wantQuant(ref, env.Values))
	if err != nil {
		return nil, err
	}
	where := "GPU"
	if gpuMem == 0 {
		where = "RAM"
	}
	env.Log("%s: %s, %.1f GB (%s %.1f GB)", ref.Repo, choice.Name, gb(choice.Size), where, gb(mem))
	for _, q := range quants {
		mark := " "
		if q.Name == choice.Name {
			mark = "▸"
		}
		env.Log("  %s %-8s %6.1f GB  need ≈ %.1f GB", mark, q.Name, gb(q.Size), gb(hf.Need(q.Size, ctxLen)))
	}
	if !choice.Fits {
		env.Log("The model doesn't fit in the GPU alone: some layers run on the CPU, which is slower.")
	}

	ngl := ""
	if gpuMem > 0 && choice.Fits {
		ngl = "999"
	}
	gpu := "0"
	if gpuMem > 0 {
		gpu = "1"
	}
	// The token goes to Hugging Face alone, not to a mirror, in a file of
	// its own that the script reads for the downloads and removes.
	endpoint, err := hf.CheckEndpoint(hf.Endpoint)
	if err != nil {
		return nil, err
	}
	tokenFile := ""
	if token := hf.TokenFor(endpoint, env.HFToken); token != "" {
		if tokenFile, err = agent.PutPrivate(ctx, env.Shell, l.ID(), "hf-token", []byte(token)); err != nil {
			return nil, err
		}
	} else if env.HFToken != "" {
		env.Log("The Hugging Face token isn't sent to %s, which isn't huggingface.co.", endpoint)
	}
	cudaURL, cudaAPI, cudaAsset, custom := cudaSource(env.Settings)
	if custom && gpu == "1" {
		env.Log("WARNING: llama.cpp for CUDA comes from %s, set in the settings. Without a checksum published beside it (%s.sha256) it runs unverified.", cudaURL, cudaURL)
	}
	alias := strings.TrimSuffix(path.Base(ref.Repo), "-GGUF")
	alias = strings.TrimSuffix(alias, "-gguf")
	proj := hf.Projector(files)
	vars := map[string]string{
		"REPO":          ref.Repo,
		"FILES":         strings.Join(choice.Files, " "),
		"MMPROJ":        proj,
		"CTX":           strconv.Itoa(ctxLen),
		"NGL":           ngl,
		"GPU":           gpu,
		"PORT":          strconv.Itoa(LlamaPort),
		"API_KEY":       env.Values["api_key"],
		"ALIAS":         alias,
		"PARALLEL":      strconv.Itoa(max(1, atoi(env.Values["parallel"], 1))),
		"EXTRA":         env.Values["args"],
		"HF_TOKEN_FILE": tokenFile,
		"HF_ENDPOINT":   endpoint,
		"CUDA_URL":      cudaURL,
		"CUDA_API":      cudaAPI,
		"CUDA_ASSET":    cudaAsset,
		"CUDA_CUSTOM":   map[bool]string{true: "1", false: ""}[custom],
	}
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	for _, k := range sortedKeys(vars) {
		fmt.Fprintf(&b, "export L_%s=%s\n", k, ShellQuote(vars[k]))
	}
	b.WriteString(llamaScript)
	if err := runScript(ctx, env, l.ID(), b.String()); err != nil {
		return nil, err
	}
	if err := waitReady(ctx, env, l.ID(), 90*time.Minute, l.readyCheck()); err != nil {
		return nil, err
	}
	env.Phase("ready")
	local := atoi(env.Values["local_port"], 8080)
	info := [][2]string{
		{"model", alias},
		{"quant", choice.Name},
		{"size", fmt.Sprintf("%.1f GB", gb(choice.Size))},
		{"context", strconv.Itoa(ctxLen)},
	}
	if !choice.Fits {
		info = append(info, [2]string{"offload", "partial"})
	}
	return &Result{
		Ports:  []Port{{Name: "llama-server", Remote: LlamaPort, Local: local, Kind: PortOpenAI, Path: "/"}},
		Model:  alias,
		APIKey: env.Values["api_key"],
		Info:   info,
	}, nil
}

func (Llama) readyCheck() string {
	return fmt.Sprintf("curl -sf --max-time 10 -o /dev/null http://127.0.0.1:%d/health", LlamaPort)
}

func (l Llama) Stop(ctx context.Context, sh *colab.Shell) error {
	return stopPlugin(ctx, sh, l.ID())
}

// gb is n bytes in GiB, as GPUs' memory is told: a 40 GB A100 has 40.
func gb(n int64) float64 { return float64(n) / (1 << 30) }

func atoi(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// ComfyHint is llama.cpp refusing a model that the comfyui plugin runs.
type ComfyHint struct {
	Repo   string
	Family string
	Err    error
}

// comfyHintText is what the error says, for SuggestComfy to find in its
// text alone, as after it's been shown and kept as a string.
const comfyHintText = "use the comfyui plugin"

func (e *ComfyHint) Error() string {
	f := comfy.FamilyByID(e.Family)
	return fmt.Sprintf("%s: %v. It is a %s model: %s to deploy it", e.Repo, e.Err, f.Name["en"], comfyHintText)
}

func (e *ComfyHint) Unwrap() error { return e.Err }

// notLLM is the error for a model llama.cpp can't serve: a ComfyHint
// when it's of a family the comfyui plugin runs.
func notLLM(repo string, info *hf.Info, err error) error {
	if f, ferr := comfy.Detect(repo, info); ferr == nil {
		return &ComfyHint{Repo: repo, Family: f.ID, Err: err}
	}
	return fmt.Errorf("%s: %w; the built-in comfyui plugin doesn't know its family, but the ComfyUI community task can run it", repo, err)
}

// SuggestComfy tells whether err is llama.cpp refusing a model the
// comfyui plugin runs, and the repository to deploy with it. It reads a
// ComfyHint, or the text of one.
func SuggestComfy(err error) (repo string, ok bool) {
	if err == nil {
		return "", false
	}
	var h *ComfyHint
	if errors.As(err, &h) {
		return h.Repo, true
	}
	msg := err.Error()
	if !strings.Contains(msg, comfyHintText) {
		return "", false
	}
	// The repository starts the text, maybe after what wrapped it.
	for _, part := range strings.Split(msg, ": ") {
		part = strings.TrimSpace(part)
		if ref, err := hf.ParseRef(part); err == nil && ref.Quant == "" && !strings.ContainsAny(part, " ") {
			return ref.Repo, true
		}
	}
	return "", true
}
