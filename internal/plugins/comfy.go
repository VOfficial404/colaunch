package plugins

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/comfy"
	"colaunch/internal/hf"
)

//go:embed comfy.sh
var comfyScript string

// ComfyPort is where ComfyUI listens on the runtime.
const ComfyPort = 8188

// The versions comfy.sh installs: a release of ComfyUI, and a commit of
// ComfyUI-GGUF, which its workflows' nodes were checked against.
const (
	ComfyUITag    = "v0.46.0"
	ComfyUICommit = "a277b5bf213e8a44fa34817b7770b54f86ef4cea"
	// The leejet fork recognizes Qwen-Image 2.1 GGUFs without architecture metadata.
	ComfyGGUF = "edd981b10e107d3b8f58e16c498f2d08f631bc47"
)

// Comfy deploys a diffusion model, as GGUF from Hugging Face, with
// ComfyUI and ComfyUI-GGUF, for images and videos made in the app or on
// ComfyUI's web page.
type Comfy struct{}

func init() {
	if err := Register(Comfy{}); err != nil {
		panic(err)
	}
}

func (Comfy) ID() string { return "comfyui" }

func (Comfy) Name() Text {
	return Text{"en": "Images and videos (ComfyUI)", "zh-Hans": "生图与生视频（ComfyUI）", "zh-Hant": "生圖與生影片（ComfyUI）"}
}

func (Comfy) Description() Text {
	return Text{
		"en":      "Runs an image or video model from Hugging Face, as GGUF, with ComfyUI: Qwen-Image, FLUX.1, Z-Image or Wan. It downloads the text encoders and VAE it needs, picks the quantization that fits the GPU, and opens a ready workflow. Colab's free tier may end runtimes that generate images through a web UI.",
		"zh-Hans": "用 ComfyUI 运行 Hugging Face 上 GGUF 格式的生图或生视频模型：Qwen-Image、FLUX.1、Z-Image 或 Wan。自动下载所需的文本编码器和 VAE，按显卡挑选放得下的量化版本，并备好可直接打开的工作流。Colab 免费层可能终止通过网页界面生成图像的运行时。",
		"zh-Hant": "用 ComfyUI 執行 Hugging Face 上 GGUF 格式的生圖或生影片模型：Qwen-Image、FLUX.1、Z-Image 或 Wan。自動下載所需的文字編碼器與 VAE，依顯示卡挑選放得下的量化版本，並備好可直接開啟的工作流程。Colab 免費層可能終止透過網頁介面生成影像的執行階段。",
	}
}

// FamilyOptions are the choices of family; "AUTO" tells it from the
// model.
func FamilyOptions() []string { return append([]string{"AUTO"}, comfy.IDs()...) }

func (Comfy) Fields() []Field {
	return []Field{
		{Key: "repo", Kind: FieldText, Required: true, Placeholder: "unsloth/Z-Image-Turbo-GGUF",
			Label: Text{"en": "Hugging Face GGUF repository", "zh-Hans": "Hugging Face GGUF 仓库", "zh-Hant": "Hugging Face GGUF 儲存庫"},
			Help:  Text{"en": "An image or video model as GGUF, user/name or its address. Add :Q4_K_M to choose a quantization.", "zh-Hans": "GGUF 格式的生图或生视频模型，填 用户/仓库 或网址，末尾加 :Q4_K_M 可指定量化。", "zh-Hant": "GGUF 格式的生圖或生影片模型，填 使用者/儲存庫 或網址，末尾加 :Q4_K_M 可指定量化。"}},
		{Key: "quant", Kind: FieldSelect, Default: "AUTO", Options: QuantOptions,
			Label: Text{"en": "Quantization", "zh-Hans": "量化版本", "zh-Hant": "量化版本"},
			Help:  Text{"en": "AUTO picks the best one that fits the machine you get.", "zh-Hans": "AUTO 会按实际分到的机器挑放得下的最佳版本。", "zh-Hant": "AUTO 會依實際分配到的機器挑放得下的最佳版本。"}},
		{Key: "family", Kind: FieldSelect, Default: "AUTO", Options: FamilyOptions(),
			Label: Text{"en": "Model family", "zh-Hans": "模型家族", "zh-Hant": "模型家族"},
			Help:  Text{"en": "AUTO tells it from the repository. Choose one for a model it can't tell.", "zh-Hans": "AUTO 会根据仓库自动识别；识别不了时请手动选择。", "zh-Hant": "AUTO 會依儲存庫自動識別；識別不了時請手動選擇。"}},
		{Key: "lora", Kind: FieldText, Advanced: true, Placeholder: "https://huggingface.co/user/repo/blob/main/lora.safetensors",
			Label: Text{"en": "LoRA (optional)", "zh-Hans": "LoRA（可选）", "zh-Hant": "LoRA（選填）"},
			Help:  Text{"en": "A .safetensors on Hugging Face, made for the same model.", "zh-Hans": "Hugging Face 上为同一模型训练的 .safetensors 文件。", "zh-Hant": "Hugging Face 上為同一模型訓練的 .safetensors 檔案。"}},
		{Key: "local_port", Kind: FieldNumber, Default: "8188", Advanced: true,
			Label: Text{"en": "Local port", "zh-Hans": "本机端口", "zh-Hant": "本機連接埠"}},
	}
}

// comfyPlan is what a deployment downloads and runs.
type comfyPlan struct {
	ref    hf.Ref
	info   *hf.Info
	family *comfy.Family
	quants []hf.Quant
	files  map[string]hf.File
	lora   *hf.File
	loraOf string
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// plan checks the values and finds the model's family and files, before
// a runtime is assigned and again on it.
func (Comfy) plan(ctx context.Context, values map[string]string, token string) (*comfyPlan, error) {
	ref, err := hf.ParseRef(values["repo"])
	if err != nil {
		return nil, err
	}
	files, err := hf.ListFiles(ctx, ref.Repo, token)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref.Repo, err)
	}
	// A mirror may not say what the model is: its name decides then.
	info, _ := hf.FetchInfo(ctx, ref.Repo, token)
	pl := &comfyPlan{ref: ref, info: info, files: map[string]hf.File{}}
	for _, f := range files {
		pl.files[f.Path] = f
	}
	id := strings.TrimSpace(values["family"])
	if id == "" || strings.EqualFold(id, "AUTO") {
		if pl.family, err = comfy.Detect(ref.Repo, info); err != nil {
			return nil, err
		}
	} else {
		if pl.family = comfy.FamilyByID(id); pl.family == nil {
			return nil, fmt.Errorf("comfy: no model family %q; there are %s", id, strings.Join(comfy.IDs(), ", "))
		}
		if err := comfy.Check(pl.family, ref.Repo, info); err != nil {
			return nil, err
		}
	}
	// The diffusion model's GGUF files, not text encoders kept beside it.
	var models []hf.File
	for _, f := range files {
		lower := strings.ToLower(f.Path)
		if strings.Contains(lower, "text_encoder") || strings.Contains(lower, "mmproj") || strings.HasPrefix(lower, "vae/") {
			continue
		}
		models = append(models, f)
	}
	pl.quants = hf.Quants(models)
	if len(pl.quants) == 0 {
		return nil, fmt.Errorf("%s has no GGUF files", ref.Repo)
	}
	if want := wantQuant(ref, values); want != "" {
		found := false
		for _, q := range pl.quants {
			found = found || q.Name == want
		}
		if !found {
			return nil, fmt.Errorf("%s has no %s file", ref.Repo, want)
		}
	}
	if l := strings.TrimSpace(values["lora"]); l != "" {
		fr, err := hf.ParseFileRef(l)
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(strings.ToLower(fr.Path), ".safetensors") {
			return nil, fmt.Errorf("comfy: the LoRA %s should be a .safetensors file", fr.Path)
		}
		lfiles, err := hf.ListFiles(ctx, fr.Repo, token)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fr.Repo, err)
		}
		for _, f := range lfiles {
			if f.Path == fr.Path {
				pl.lora, pl.loraOf = &f, fr.Repo
				break
			}
		}
		if pl.lora == nil {
			return nil, fmt.Errorf("comfy: %s has no file %s", fr.Repo, fr.Path)
		}
		if !safeFilePath(fr.Path) {
			return nil, fmt.Errorf("comfy: the LoRA's name %q has characters it can't keep", path.Base(fr.Path))
		}
	}
	return pl, nil
}

func (c Comfy) Validate(ctx context.Context, values map[string]string, hfToken string) (*Hardware, error) {
	pl, err := c.plan(ctx, values, hfToken)
	if err != nil {
		return nil, err
	}
	q := pl.quants[0]
	want := wantQuant(pl.ref, values)
	for _, x := range pl.quants {
		if want != "" && x.Name == want {
			q = x
			break
		}
		if want == "" && x.Size < q.Size {
			q = x
		}
	}
	return &Hardware{Variant: colab.GPU, MinMemory: pl.family.NeedLeast(q.Size)}, nil
}

// comfyChoice picks the quantization for the GPU: one that fits with
// everything on it, else one that fits once ComfyUI takes the text
// encoders off it, else the smallest, which keeps some of its weights in
// the machine's memory.
func comfyChoice(f *comfy.Family, quants []hf.Quant, vram, ram int64, want string) (hf.Choice, string, error) {
	if vram == 0 {
		return hf.Choice{}, "", errors.New("comfy: the runtime has no GPU, which ComfyUI's models need")
	}
	if want != "" {
		c, err := hf.PickBy(quants, vram, 0, want, f.Need)
		if err != nil {
			return c, "", err
		}
		switch {
		case c.Fits:
			return c, "all", nil
		case f.NeedLeast(c.Size) <= vram:
			c.Fits = true
			return c, "swap", nil
		}
		return c, "offload", nil
	}
	if c, err := hf.PickBy(quants, vram, 0, "", f.Need); err == nil {
		return c, "all", nil
	}
	if c, err := hf.PickBy(quants, vram, 0, "", f.NeedLeast); err == nil {
		return c, "swap", nil
	}
	// Each step streams what's in the machine's memory to the GPU: the
	// smallest quantization keeps the least there.
	smallest := -1
	for i, q := range quants {
		if f.NeedLeast(q.Size) <= vram+ram/2 && (smallest < 0 || q.Size < quants[smallest].Size) {
			smallest = i
		}
	}
	if smallest < 0 {
		return hf.Choice{}, "", hf.ErrTooBig
	}
	return hf.Choice{Quant: quants[smallest]}, "offload", nil
}

func (c Comfy) Deploy(ctx context.Context, env *Env) (*Result, error) {
	env.Phase("resolve")
	pl, err := c.plan(ctx, env.Values, env.HFToken)
	if err != nil {
		return nil, err
	}
	f := pl.family
	vram := env.Status.VRAMMB() << 20
	choice, fit, err := comfyChoice(f, pl.quants, vram, env.Status.Memory.Available, wantQuant(pl.ref, env.Values))
	if err != nil {
		return nil, err
	}
	if len(choice.Files) != 1 {
		return nil, fmt.Errorf("%s: its %s comes in %d parts, which ComfyUI-GGUF doesn't read", pl.ref.Repo, choice.Name, len(choice.Files))
	}
	env.Log("%s: %s, %s, %.1f GB (GPU %.1f GB)", pl.ref.Repo, f.Name["en"], choice.Name, gb(choice.Size), gb(vram))
	for _, q := range pl.quants {
		mark := " "
		if q.Name == choice.Name {
			mark = "▸"
		}
		env.Log("  %s %-8s %6.1f GB  need ≈ %.1f GB (%.1f GB at the least)", mark, q.Name, gb(q.Size), gb(f.Need(q.Size)), gb(f.NeedLeast(q.Size)))
	}
	switch fit {
	case "swap":
		env.Log("The text encoders don't fit beside the model: ComfyUI moves them off the GPU once they've read the prompt, which takes a few seconds more.")
	case "offload":
		env.Log("The model doesn't fit in the GPU: ComfyUI keeps some of its weights in the machine's memory, which is slower.")
	}

	endpoint, err := hf.CheckEndpoint(hf.Endpoint)
	if err != nil {
		return nil, err
	}
	main := pl.files[choice.Files[0]]
	if !safeName.MatchString(path.Base(main.Path)) || !safeFilePath(main.Path) {
		return nil, fmt.Errorf("comfy: the file's name %q has characters it can't keep", main.Path)
	}
	repoDir := strings.ReplaceAll(pl.ref.Repo, "/", "__")
	m := comfy.Models{UNet: "colaunch/" + repoDir + "/" + path.Base(main.Path)}
	rev := "main"
	if pl.info != nil && pl.info.SHA != "" {
		rev = pl.info.SHA
	}
	// What to download: "url sha256 size dest token", dest under
	// ComfyUI's models/. Only the user's own repositories, the model's
	// and the LoRA's, get the token: the companions are public.
	var lines []string
	add := func(repo, revision, file, sha string, size int64, dest string, token bool) {
		t := ""
		if token {
			t = "1"
		}
		lines = append(lines, strings.Join([]string{fmt.Sprintf("%s/%s/resolve/%s/%s", endpoint, repo, revision, file), orDash(sha), strconv.FormatInt(size, 10), dest, orDash(t)}, " "))
	}
	add(pl.ref.Repo, rev, main.Path, main.SHA256, main.Size, "diffusion_models/"+m.UNet, true)
	for _, cf := range f.Files {
		add(cf.Repo, cf.Revision, cf.Path, cf.SHA256, cf.Size, cf.Dir+"/"+cf.Name(), false)
	}
	if pl.lora != nil {
		m.LoRA = "colaunch/" + strings.ReplaceAll(pl.loraOf, "/", "__") + "/" + path.Base(pl.lora.Path)
		m.LoRAStrength = 1
		add(pl.loraOf, "main", pl.lora.Path, pl.lora.SHA256, pl.lora.Size, "loras/"+m.LoRA, true)
	}

	alias := strings.TrimSuffix(strings.TrimSuffix(path.Base(pl.ref.Repo), "-GGUF"), "-gguf")
	deployment, _ := json.Marshal(comfy.Deployment{Family: f.ID, UNet: m.UNet, LoRA: m.LoRA, LoRAStrength: m.LoRAStrength, Model: alias, Quant: choice.Name})
	p := f.Defaults
	p.Prompt = examplePrompt(f)
	ui, err := f.Workflow(m, p).UI(endpoint)
	if err != nil {
		return nil, err
	}

	tokenFile := ""
	if token := hf.TokenFor(endpoint, env.HFToken); token != "" {
		if tokenFile, err = agent.PutPrivate(ctx, env.Shell, c.ID(), "hf-token", []byte(token)); err != nil {
			return nil, err
		}
	} else if env.HFToken != "" {
		env.Log("The Hugging Face token isn't sent to %s, which isn't huggingface.co.", endpoint)
	}
	vars := map[string]string{
		"FAMILY":         f.ID,
		"DOWNLOADS":      strings.Join(lines, "\n"),
		"DEPLOYMENT":     base64.StdEncoding.EncodeToString(deployment),
		"WORKFLOW":       base64.StdEncoding.EncodeToString(ui),
		"PORT":           strconv.Itoa(ComfyPort),
		"COMFY_TAG":      ComfyUITag,
		"COMFY_COMMIT":   ComfyUICommit,
		"GGUF_COMMIT":    ComfyGGUF,
		"HF_TOKEN_FILE":  tokenFile,
		"HF_ENDPOINT":    endpoint,
		"OFFLOAD":        map[bool]string{true: "1", false: ""}[fit == "offload"],
		"COMFY_UPSTREAM": "https://github.com/Comfy-Org/ComfyUI.git",
		"GGUF_UPSTREAM":  "https://github.com/leejet/ComfyUI-GGUF.git",
	}
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	for _, k := range sortedKeys(vars) {
		fmt.Fprintf(&b, "export C_%s=%s\n", k, ShellQuote(vars[k]))
	}
	b.WriteString(comfyScript)
	if err := runScript(ctx, env, c.ID(), b.String()); err != nil {
		return nil, err
	}
	if err := waitReady(ctx, env, c.ID(), 90*time.Minute, c.readyCheck(ComfyPort)); err != nil {
		return nil, err
	}
	env.Phase("ready")
	info := [][2]string{
		{"model", alias},
		{"family", f.ID},
		{"quant", choice.Name},
		{"size", fmt.Sprintf("%.1f GB", gb(choice.Size))},
		{"workflow", "colaunch-" + f.ID + ".json"},
	}
	if fit != "all" {
		info = append(info, [2]string{"offload", fit})
	}
	if m.LoRA != "" {
		info = append(info, [2]string{"lora", path.Base(m.LoRA)})
	}
	return &Result{
		Ports: []Port{{Name: "ComfyUI", Remote: ComfyPort, Local: atoi(env.Values["local_port"], ComfyPort), Kind: PortComfy, Path: "/"}},
		Model: alias,
		Info:  info,
	}, nil
}

func (Comfy) readyCheck(port int) string {
	return fmt.Sprintf("curl -sf -o /dev/null http://127.0.0.1:%d/system_stats", port)
}

func (c Comfy) Stop(ctx context.Context, sh *colab.Shell) error {
	return stopPlugin(ctx, sh, c.ID())
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// safeFilePath tells whether a repository's file path can go in a URL
// and a line of the script as it is.
func safeFilePath(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || !safeName.MatchString(part) {
			return false
		}
	}
	return true
}

// examplePrompt is the prompt the web page's workflow opens with.
func examplePrompt(f *comfy.Family) string {
	if f.Kind == comfy.Video {
		return "a red fox trotting through fresh snow in a pine forest, morning light, tracking shot"
	}
	return "a cozy reading nook by a rainy window, warm lamp light, a cat asleep on a pile of books, detailed, soft focus background"
}
