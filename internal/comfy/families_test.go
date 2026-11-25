package comfy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"colaunch/internal/hf"
)

// What Hugging Face says of real repositories (2026-11-25), and the
// family each is.
func TestDetect(t *testing.T) {
	info := func(arch, base string) *hf.Info {
		return &hf.Info{Architecture: arch, Tags: []string{"gguf", "base_model:" + base, "base_model:quantized:" + base}}
	}
	for _, tc := range []struct {
		repo   string
		info   *hf.Info
		family string
	}{
		{"city96/Qwen-Image-gguf", info("qwen_image", "Qwen/Qwen-Image"), "qwen-image"},
		{"QuantStack/Qwen-Image-GGUF", info("qwen_image", "Qwen/Qwen-Image"), "qwen-image"},
		{"unsloth/Qwen-Image-2512-GGUF", info("qwen_image", "Qwen/Qwen-Image-2512"), "qwen-image"},
		{"abenzerps/Qwen-Image-2.1-Turbo-Quantized", &hf.Info{
			Architecture: "qwen_image",
			Pipeline:     "text-to-image",
			Tags: []string{"gguf", "qwen", "image-generation", "comfyui", "comfyui-gguf", "mlx", "fp8", "nvfp4", "text-to-image",
				"base_model:Qwen/Qwen-Image-2.1-Turbo", "base_model:quantized:Qwen/Qwen-Image-2.1-Turbo", "license:other", "region:us"},
		}, "qwen-image-2.1-turbo"},
		{"unsloth/Qwen-Image-2.1-Turbo-GGUF", info("qwen_image", "Qwen/Qwen-Image-2.1-Turbo"), "qwen-image-2.1-turbo"},
		{"unsloth/Qwen-Image-2.1-GGUF", info("", "Qwen/Qwen-Image-2.1"), "qwen-image-2.1"},
		{"leejet/Qwen-Image-2.1-GGUF", info("qwen_image21", "Qwen/Qwen-Image-2.1"), "qwen-image-2.1"},
		{"mirror/Qwen_Image_2_1_Turbo-GGUF", nil, "qwen-image-2.1-turbo"},
		{"city96/FLUX.1-dev-gguf", info("flux", "black-forest-labs/FLUX.1-dev"), "flux1-dev"},
		{"city96/FLUX.1-schnell-gguf", info("flux", "black-forest-labs/FLUX.1-schnell"), "flux1-schnell"},
		{"unsloth/Z-Image-Turbo-GGUF", info("lumina2", "Tongyi-MAI/Z-Image-Turbo"), "z-image-turbo"},
		{"someone/Z-Image-GGUF", info("lumina2", "Tongyi-MAI/Z-Image"), "z-image"},
		{"city96/Wan2.1-T2V-14B-gguf", info("wan", "Wan-AI/Wan2.1-T2V-14B"), "wan21-t2v"},
		{"samuelchristlie/Wan2.1-T2V-1.3B-GGUF", info("wan", "Wan-AI/Wan2.1-T2V-1.3B"), "wan21-t2v"},
		{"QuantStack/Wan2.2-TI2V-5B-GGUF", info("wan", "Wan-AI/Wan2.2-TI2V-5B"), "wan22-ti2v-5b"},
		// A mirror says nothing: the name decides.
		{"unsloth/Wan2.2-TI2V-5B-GGUF", nil, "wan22-ti2v-5b"},
	} {
		f, err := Detect(tc.repo, tc.info)
		if err != nil || f.ID != tc.family {
			t.Errorf("%s: %v, %v; want %s", tc.repo, f, err, tc.family)
		}
	}
	for repo, inf := range map[string]*hf.Info{
		"QuantStack/Wan2.2-T2V-A14B-GGUF":        info("wan", "Wan-AI/Wan2.2-T2V-A14B"),
		"QuantStack/Qwen-Image-Edit-GGUF":        info("qwen_image", "Qwen/Qwen-Image-Edit"),
		"mirror/Qwen-Image-2.1-Turbo-Edit-GGUF":  info("qwen_image21", "Qwen/Qwen-Image-2.1-Turbo"),
		"city96/Wan2.1-I2V-14B-480P-gguf":        info("wan", "Wan-AI/Wan2.1-I2V-14B-480P"),
		"silveroxides/Chroma-GGUF":               info("flux", "lodestones/Chroma"),
		"someone/Lumina-Image-2.0-GGUF":          info("lumina2", "Alpha-VLLM/Lumina-Image-2.0"),
		"city96/stable-diffusion-3.5-large-gguf": info("sd3", "stabilityai/stable-diffusion-3.5-large"),
		// The name says FLUX.1 [dev], the file says otherwise.
		"someone/FLUX.1-dev-GGUF": info("qwen_image", "black-forest-labs/FLUX.1-dev"),
	} {
		f, err := Detect(repo, inf)
		if !errors.Is(err, ErrUnknownFamily) {
			t.Errorf("%s: %v, %v; want ErrUnknownFamily", repo, f, err)
		}
	}
	if _, err := Detect("QuantStack/Wan2.2-T2V-A14B-GGUF", info("wan", "Wan-AI/Wan2.2-T2V-A14B")); !strings.Contains(err.Error(), "high and low noise") {
		t.Errorf("A14B: %v", err)
	}
	// A family chosen by hand must match the file.
	if Check(FamilyByID("flux1-dev"), "x/y", info("wan", "z")) == nil || Check(FamilyByID("flux1-dev"), "x/y", info("flux", "z")) != nil {
		t.Error("Check")
	}
}

func TestFamiliesAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Families() {
		if seen[f.ID] || FamilyByID(f.ID) != f {
			t.Errorf("%s: duplicate", f.ID)
		}
		seen[f.ID] = true
		for _, lang := range []string{"en", "zh-Hans", "zh-Hant"} {
			if f.Name[lang] == "" {
				t.Errorf("%s: no %s name", f.ID, lang)
			}
		}
		if f.Kind != Image && f.Kind != Video || len(f.Sizes) == 0 || len(f.Archs) == 0 || len(f.Files) == 0 {
			t.Errorf("%s: incomplete", f.ID)
		}
		for _, c := range f.Files {
			if len(c.SHA256) != 64 || len(c.Revision) != 40 || c.Size <= 0 || c.Dir == "" {
				t.Errorf("%s: %s isn't pinned", f.ID, c.Path)
			}
		}
		for _, s := range f.Sizes {
			if s[0]%f.Step != 0 && f.Step == 32 || s[0]%16 != 0 || s[1]%16 != 0 {
				t.Errorf("%s: size %v", f.ID, s)
			}
		}
	}
}

func TestFill(t *testing.T) {
	wan := FamilyByID("wan21-t2v")
	p := wan.Fill(Params{Prompt: "x", Width: 830, Frames: 30})
	if p.Width != 832 || p.Height != 480 || p.Frames != 29 || p.FPS != 16 || p.Steps != 30 || p.Negative == "" {
		t.Errorf("wan: %+v", p)
	}
	if p := wan.Fill(Params{Frames: 1000}); p.Frames != 121 {
		t.Errorf("frames %d", p.Frames)
	}
	schnell := FamilyByID("flux1-schnell")
	p = schnell.Fill(Params{Negative: "blurry", Count: 20})
	if p.Negative != "" || p.Steps != 4 || p.Count != 8 || p.Frames != 0 {
		t.Errorf("schnell: %+v", p)
	}
	if p := FamilyByID("wan22-ti2v-5b").Fill(Params{Width: 1270, Height: 700}); p.Width != 1280 || p.Height != 704 {
		t.Errorf("wan 2.2: %+v", p)
	}
}

// Every family's workflow is connected: each link goes to a node there,
// to an output it has, and the prompt, size and seed get in.
func TestWorkflows(t *testing.T) {
	for _, f := range Families() {
		g := f.Workflow(Models{UNet: "colaunch/a__b/m-Q4_K_M.gguf", LoRA: "colaunch/l.safetensors"}, Params{Prompt: "a fox", Seed: 42})
		api := g.API()
		var saw struct{ prompt, seed, unet, lora, save bool }
		for id, n := range api {
			node := n.(map[string]any)
			class := node["class_type"].(string)
			if _, ok := nodeTypes[class]; !ok {
				t.Errorf("%s: node %s: %s has no layout", f.ID, id, class)
			}
			for k, v := range node["inputs"].(map[string]any) {
				if l, ok := v.([]any); ok {
					from, _ := api[l[0].(string)].(map[string]any)
					if from == nil || len(nodeTypes[from["class_type"].(string)].outputs) <= l[1].(int) {
						t.Errorf("%s: %s.%s links to nothing", f.ID, class, k)
					}
				}
				switch {
				case (k == "text" || k == "prompt") && v == "a fox":
					saw.prompt = true
				case (k == "seed" || k == "noise_seed") && v == int64(42):
					saw.seed = true
				case k == "unet_name" && v == "colaunch/a__b/m-Q4_K_M.gguf":
					saw.unet = true
				case k == "lora_name":
					saw.lora = true
				}
			}
			saw.save = saw.save || class == "SaveImage" || class == "SaveVideo"
		}
		if saw != (struct{ prompt, seed, unet, lora, save bool }{true, true, true, true, true}) {
			t.Errorf("%s: %+v", f.ID, saw)
		}
		ui, err := g.UI("https://huggingface.co")
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Nodes []struct {
				Type    string `json:"type"`
				Widgets []any  `json:"widgets_values"`
			} `json:"nodes"`
			Links [][]any `json:"links"`
		}
		if err := json.Unmarshal(ui, &doc); err != nil || len(doc.Nodes) != len(api) || len(doc.Links) == 0 {
			t.Errorf("%s: UI workflow: %v, %d nodes", f.ID, err, len(doc.Nodes))
		}
		if strings.Count(string(ui), "/resolve/") < len(f.Files) {
			t.Errorf("%s: the UI workflow doesn't say where its files come from", f.ID)
		}
	}
	// A random seed when none is asked for.
	f := FamilyByID("z-image-turbo")
	a, b := f.Workflow(Models{UNet: "m.gguf"}, Params{Seed: -1}), f.Workflow(Models{UNet: "m.gguf"}, Params{Seed: -1})
	if seedOf(a) == seedOf(b) {
		t.Error("the same random seed twice")
	}
}

func TestQwen21TurboSchedule(t *testing.T) {
	f := FamilyByID("qwen-image-2.1-turbo")
	if d := f.Defaults; d.Steps != 8 || d.CFG != 1 || d.Width != 1024 || d.Height != 1024 {
		t.Fatalf("Turbo defaults: %+v", d)
	}
	for _, tc := range []struct {
		steps   int
		sampler string
	}{
		{0, "SamplerCustomAdvanced"},
		{12, "KSampler"},
	} {
		g := f.Workflow(Models{UNet: "turbo.gguf"}, Params{Steps: tc.steps, Seed: 42})
		byClass := map[string]*Node{}
		for _, n := range g.Nodes {
			byClass[n.Class] = n
		}
		sampler := byClass[tc.sampler]
		if sampler == nil {
			t.Fatalf("steps %d: no %s", tc.steps, tc.sampler)
		}
		if tc.steps == 0 {
			sigmas := g.Node(sampler.Inputs["sigmas"].(Link).Node)
			if sigmas.Class != "ManualSigmas" || sigmas.Inputs["sigmas"] != "1.0, 0.978453, 0.95418, 0.926626, 0.89508, 0.845148, 0.704534, 0.414568, 0" {
				t.Errorf("Turbo sigmas: %+v", sigmas)
			}
			if guider := byClass["CFGGuider"]; guider == nil || guider.Inputs["cfg"] != 1.0 {
				t.Errorf("Turbo guider: %+v", guider)
			}
		} else if sampler.Inputs["steps"] != tc.steps || sampler.Inputs["scheduler"] != "simple" {
			t.Errorf("Turbo sampler at %d steps: %+v", tc.steps, sampler.Inputs)
		}
	}
}

func seedOf(g *Graph) any {
	for _, n := range g.Nodes {
		if s, ok := n.Inputs["seed"]; ok {
			return s
		}
	}
	return nil
}

// The memory a model needs counts its companions: Qwen-Image's Q4_K_M,
// 12 GB, doesn't fit a T4 with its 9 GB text encoder, but runs on it once
// ComfyUI moves the encoder off the GPU.
func TestNeed(t *testing.T) {
	f := FamilyByID("qwen-image")
	q4 := int64(12_170_000_000)
	t4 := int64(15360) << 20
	if f.Need(q4) <= t4 {
		t.Errorf("Need %d fits a T4", f.Need(q4))
	}
	if f.NeedLeast(q4) > t4 || f.NeedLeast(q4) >= f.Need(q4) {
		t.Errorf("NeedLeast %d", f.NeedLeast(q4))
	}
}
