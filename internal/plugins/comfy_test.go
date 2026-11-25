package plugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"colaunch/internal/comfy"
	"colaunch/internal/hf"
)

// fakeHF answers as Hugging Face does for a few repositories.
func fakeHF(t *testing.T) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/city96/Qwen-Image-gguf":
			w.Write([]byte(`{"gguf":{"architecture":"qwen_image"},"pipeline_tag":"text-to-image","tags":["gguf","base_model:Qwen/Qwen-Image","base_model:quantized:Qwen/Qwen-Image"],"sha":"e77babc55af111419e1714a7a0a848b9cac25db7"}`))
		case "/api/models/city96/Qwen-Image-gguf/tree/main":
			w.Write([]byte(`[{"type":"file","path":"qwen-image-Q4_K_M.gguf","size":135,"lfs":{"oid":"1111111111111111111111111111111111111111111111111111111111111111","size":12170000000}},
				{"type":"file","path":"qwen-image-Q8_0.gguf","size":135,"lfs":{"oid":"2222222222222222222222222222222222222222222222222222222222222222","size":20270000000}},
				{"type":"file","path":"qwen-image-Q2_K.gguf","size":135,"lfs":{"oid":"3333333333333333333333333333333333333333333333333333333333333333","size":6580000000}},
				{"type":"file","path":"README.md","size":100}]`))
		case "/api/models/a/sd35":
			w.Write([]byte(`{"gguf":{"architecture":"sd3"},"pipeline_tag":"text-to-image","tags":["base_model:stabilityai/stable-diffusion-3.5-large"]}`))
		case "/api/models/a/sd35/tree/main":
			w.Write([]byte(`[{"type":"file","path":"sd3.5_large-Q4_0.gguf","size":10,"lfs":{"oid":"","size":4800000000}}]`))
		case "/api/models/a/lora/tree/main":
			w.Write([]byte(`[{"type":"file","path":"loras/style.safetensors","size":10,"lfs":{"oid":"4444444444444444444444444444444444444444444444444444444444444444","size":300000000}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldEndpoint, oldClient := hf.Endpoint, hf.HTTP
	hf.Endpoint, hf.HTTP = srv.URL, srv.Client()
	t.Cleanup(func() { hf.Endpoint, hf.HTTP = oldEndpoint, oldClient })
}

// Before a runtime is assigned, the family is told, the quantization
// asked for checked, and the least memory advised; a model of no family
// is refused then, not on the runtime.
func TestComfyValidate(t *testing.T) {
	fakeHF(t)
	ctx := context.Background()
	c := Comfy{}
	hw, err := c.Validate(ctx, map[string]string{"repo": "city96/Qwen-Image-gguf", "quant": "AUTO", "family": "AUTO"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := comfy.FamilyByID("qwen-image").NeedLeast(6580000000); hw.MinMemory != want {
		t.Errorf("MinMemory %d, want %d, the Q2_K's", hw.MinMemory, want)
	}
	if _, err := c.Validate(ctx, map[string]string{"repo": "city96/Qwen-Image-gguf:Q5_K_M"}, ""); err == nil || !strings.Contains(err.Error(), "no Q5_K_M") {
		t.Errorf("missing quant: %v", err)
	}
	if _, err := c.Validate(ctx, map[string]string{"repo": "a/sd35", "family": "AUTO"}, ""); !errors.Is(err, comfy.ErrUnknownFamily) || !strings.Contains(err.Error(), "choose its family") {
		t.Errorf("unknown family: %v", err)
	}
	if _, err := c.Validate(ctx, map[string]string{"repo": "a/sd35", "family": "flux1-dev"}, ""); err == nil {
		t.Error("an sd3 model passed for FLUX.1")
	}
	// The LoRA is checked too.
	ok := map[string]string{"repo": "city96/Qwen-Image-gguf", "lora": "https://huggingface.co/a/lora/blob/main/loras/style.safetensors"}
	if _, err := c.Validate(ctx, ok, ""); err != nil {
		t.Errorf("LoRA: %v", err)
	}
	for _, bad := range []string{"a/lora/loras/missing.safetensors", "a/lora/loras/style.ckpt", "https://example.com/a/lora/x.safetensors", "a/lora/../x.safetensors"} {
		if _, err := c.Validate(ctx, map[string]string{"repo": "city96/Qwen-Image-gguf", "lora": bad}, ""); err == nil {
			t.Errorf("LoRA %s passed", bad)
		}
	}
}

// llama.cpp refuses an image model, and says to use the comfyui plugin
// when it's of a family the plugin runs; SuggestComfy finds the
// repository, from the error or from its text alone.
func TestLlamaSuggestsComfy(t *testing.T) {
	fakeHF(t)
	_, err := Llama{}.Validate(context.Background(), map[string]string{"repo": "city96/Qwen-Image-gguf", "ctx": "8192"}, "")
	if !errors.Is(err, hf.ErrNotLLM) || !strings.Contains(err.Error(), "use the comfyui plugin") {
		t.Fatalf("%v", err)
	}
	for _, e := range []error{err, errors.New(err.Error()), errors.New("deploy failed: " + err.Error())} {
		if repo, ok := SuggestComfy(e); !ok || repo != "city96/Qwen-Image-gguf" {
			t.Errorf("SuggestComfy(%q) = %q, %v", e, repo, ok)
		}
	}
	_, err = Llama{}.Validate(context.Background(), map[string]string{"repo": "a/sd35"}, "")
	if !errors.Is(err, hf.ErrNotLLM) || strings.Contains(err.Error(), "use the comfyui plugin") {
		t.Errorf("sd3: %v", err)
	}
	if _, ok := SuggestComfy(err); ok {
		t.Error("suggested the comfyui plugin for a family it doesn't run")
	}
	if _, ok := SuggestComfy(errors.New("network down")); ok {
		t.Error("suggested for any error")
	}
}

// The quantization fits the GPU with its text encoders, else without
// them, else partly in the machine's memory.
func TestComfyChoice(t *testing.T) {
	f := comfy.FamilyByID("qwen-image")
	quants := hf.Quants([]hf.File{
		{Path: "q-Q8_0.gguf", Size: 20_270_000_000}, {Path: "q-Q4_K_M.gguf", Size: 12_170_000_000}, {Path: "q-Q2_K.gguf", Size: 6_580_000_000},
	})
	gib := int64(1 << 30)
	for _, tc := range []struct {
		vram      int64
		name, fit string
	}{
		{80 * gib, "Q8_0", "all"},
		{40 * gib, "Q8_0", "all"},
		{24 * gib, "Q4_K_M", "all"},
		{15 * gib, "Q4_K_M", "swap"},
		{8 * gib, "Q2_K", "offload"},
	} {
		c, fit, err := comfyChoice(f, quants, tc.vram, 50*gib, "")
		if err != nil || c.Name != tc.name || fit != tc.fit {
			t.Errorf("%d GB: %s %s %v, want %s %s", tc.vram/gib, c.Name, fit, err, tc.name, tc.fit)
		}
	}
	if _, _, err := comfyChoice(f, quants, 0, 50*gib, ""); err == nil {
		t.Error("chose for no GPU")
	}
	if c, fit, err := comfyChoice(f, quants, 15*gib, 50*gib, "Q8_0"); err != nil || c.Name != "Q8_0" || fit != "offload" {
		t.Errorf("asked Q8_0: %s %s %v", c.Name, fit, err)
	}
}

func TestComfyFields(t *testing.T) {
	keys := map[string]bool{}
	for _, f := range (Comfy{}).Fields() {
		keys[f.Key] = true
		if f.Key == "family" && (f.Options[0] != "AUTO" || len(f.Options) != len(comfy.Families())+1) {
			t.Errorf("family options %v", f.Options)
		}
	}
	for _, k := range []string{"repo", "quant", "family", "lora", "local_port"} {
		if !keys[k] {
			t.Errorf("no field %s", k)
		}
	}
	if Get("comfyui") == nil {
		t.Error("the comfyui plugin isn't registered")
	}
}
