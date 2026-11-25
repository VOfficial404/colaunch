package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/colab"
	"colaunch/internal/comfy"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

// familyOf is the first family of a kind.
func familyOf(t *testing.T, k comfy.Kind) *comfy.Family {
	for _, f := range comfy.Families() {
		if f.Kind == k {
			return f
		}
	}
	t.Fatalf("no family of kind %q", k)
	return nil
}

// addComfy adds a runtime that runs ComfyUI with a family of models.
func addComfy(a *App, id string, f *comfy.Family) *core.Session {
	s := &core.Session{Phase: core.PhaseRunning}
	s.ID, s.Account, s.Plugin = id, "ada@example.com", comfyPlugin
	s.Spec = colab.Spec{Variant: colab.GPU, Accelerator: "L4"}
	s.Created = time.Now().Add(-12 * time.Minute)
	s.GPUName, s.VRAM = "NVIDIA L4", 23034
	s.Values = map[string]string{comfyRepo: "Comfy-Org/" + f.ID}
	s.Result = &plugins.Result{
		Model: familyName(f),
		Ports: []plugins.Port{{Name: "ComfyUI", Remote: 8188, Local: 8188, Kind: plugins.PortComfy}},
		Info:  [][2]string{{"family", f.ID}},
	}
	a.m.Sessions = append(a.m.Sessions, s)
	return s
}

func testPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(255 * x / w), uint8(120 + 100*y/h), 60, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// createViews are the Create page's states, in order: each builds on
// the one before.
func createViews(t *testing.T, a *App) []struct {
	name  string
	setup func()
} {
	img, vid := familyOf(t, kindImage), familyOf(t, kindVideo)
	var s *core.Session
	return []struct {
		name  string
		setup func()
	}{
		{"empty", func() { a.page = pageCreate }},
		{"form", func() {
			s = addComfy(a, "e5", img)
			addComfy(a, "f6", vid)
			a.create.addr = func(*core.Session) string { return "127.0.0.1:8188" }
			a.create.session = s.ID
			a.page = pageCreate
		}},
		{"video", func() { a.create.session = "f6" }},
		{"generating", func() {
			a.create.session = s.ID
			a.create.init()
			a.create.form(img).prompt = "a red fox in the snow, golden hour"
			a.create.jobs[s.ID] = &createJob{family: img, started: time.Now().Add(-14 * time.Second), prog: comfy.Progress{Step: 12, Steps: 30, Node: "KSampler", Queue: 1}}
		}},
		{"results", func() {
			delete(a.create.jobs, s.ID)
			a.create.errs[s.ID] = "comfy: torch.OutOfMemoryError: CUDA out of memory. Tried to allocate 2.00 GiB"
			for i, sz := range [][2]int{{96, 96}, {128, 72}, {72, 128}} {
				it, err := newCreation(comfy.Output{Filename: fmt.Sprintf("ComfyUI_%05d_.png", i), Kind: kindImage, MIME: "image/png", Data: testPNG(sz[0], sz[1])}, i, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				it.session, it.family = s.ID, img
				it.params = comfy.Params{Prompt: "a red fox in the snow, golden hour", Width: sz[0] * 8, Height: sz[1] * 8, Steps: 30, CFG: 7, Seed: 421337}
				a.create.add(it)
			}
			clip := &creation{session: "f6", family: vid, kind: kindVideo, mime: "video/mp4", ext: ".mp4", path: "missing.mp4",
				params: comfy.Params{Prompt: "waves at dusk", Width: 832, Height: 480, Steps: 30, CFG: 6, Seed: 7, Frames: 81, FPS: 16}}
			a.create.add(clip)
		}},
		{"runtime", func() { a.page = pageRuntimes }},
	}
}

// fakeComfy is a ComfyUI that makes one picture of each prompt, or, when
// hold is set, runs until it is interrupted.
type fakeComfy struct {
	*httptest.Server
	mu          sync.Mutex
	hold        bool
	family      string
	prompts     []string
	interrupted chan struct{}
	done        bool
}

func newFakeComfy(t *testing.T, hold bool) *fakeComfy {
	fc := &fakeComfy{hold: hold, family: familyOf(t, kindImage).ID, interrupted: make(chan struct{}, 4)}
	pic := testPNG(64, 64)
	mux := http.NewServeMux()
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"system":{"os":"posix","comfyui_version":"0.3.40"},"devices":[{"name":"cuda:0 NVIDIA L4","type":"cuda","vram_total":24152899584,"vram_free":23000000000}]}`))
	})
	mux.HandleFunc("/userdata/colaunch.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"family": fc.family, "unet": "model-Q4_K_M.gguf"})
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"queue_running":[],"queue_pending":[]}`))
	})
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Write([]byte(`{"exec_info":{"queue_remaining":0}}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		fc.mu.Lock()
		fc.prompts = append(fc.prompts, string(b))
		n := len(fc.prompts)
		fc.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"prompt_id": fmt.Sprintf("p%d", n), "number": n, "node_errors": map[string]any{}})
	})
	mux.HandleFunc("/history/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		fc.mu.Lock()
		hold, done := fc.hold, fc.done
		fc.mu.Unlock()
		switch {
		case hold && !done:
			w.Write([]byte(`{}`))
		case hold:
			fmt.Fprintf(w, `{%q:{"outputs":{},"status":{"status_str":"error","completed":false,"messages":[["execution_interrupted",{"prompt_id":%q}]]}}}`, id, id)
		default:
			fmt.Fprintf(w, `{%q:{"outputs":{"9":{"images":[{"filename":"ComfyUI_00001_.png","subfolder":"","type":"output"}]}},"status":{"status_str":"success","completed":true,"messages":[]}}}`, id)
		}
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pic)
	})
	mux.HandleFunc("/interrupt", func(w http.ResponseWriter, r *http.Request) {
		fc.mu.Lock()
		fc.done = true
		fc.mu.Unlock()
		fc.interrupted <- struct{}{}
	})
	fc.Server = httptest.NewServer(mux)
	t.Cleanup(fc.Close)
	return fc
}

// createTestApp is an app on the Create page of a runtime whose ComfyUI is
// fc, whose posts from goroutines wait in posted.
func createTestApp(t *testing.T, fc *fakeComfy) (*App, *core.Session, *comfy.Family, chan func()) {
	i18n.SetLang("en")
	a := newTestApp(t)
	f := familyOf(t, kindImage)
	s := addComfy(a, "e5", f)
	a.create.addr = func(*core.Session) string { return strings.TrimPrefix(fc.URL, "http://") }
	a.create.dir = t.TempDir()
	a.page = pageCreate
	posted := make(chan func(), 64)
	a.m.Post = func(fn func()) { posted <- fn }
	return a, s, f, posted
}

// pump runs what goroutines posted until done says so.
func pump(t *testing.T, posted chan func(), done func() bool) {
	deadline := time.After(15 * time.Second)
	for !done() {
		select {
		case fn := <-posted:
			fn()
		case <-deadline:
			t.Fatal("timed out")
		}
	}
}

// Generating sends the prompt to ComfyUI and shows what it made.
func TestCreateGenerates(t *testing.T) {
	fc := newFakeComfy(t, false)
	a, s, f, posted := createTestApp(t, fc)
	tt := ui.NewTester(a.view, 1120, 1600)
	tt.Frame()
	a.create.form(f).prompt = "a red fox in the snow"
	if err := tt.Click("Generate"); err != nil {
		t.Fatal(err)
	}
	if a.create.jobs[s.ID] == nil {
		t.Fatalf("nothing started: %q", a.create.errs[s.ID])
	}
	tt.Frame()
	pump(t, posted, func() bool { return a.create.jobs[s.ID] == nil })
	tt.Frame()
	if e := a.create.errs[s.ID]; e != "" {
		t.Fatalf("error %q", e)
	}
	if len(a.create.items) == 0 || a.create.items[0].bitmap == nil {
		t.Fatalf("results %v", a.create.items)
	}
	fc.mu.Lock()
	sent := strings.Join(fc.prompts, "\n")
	fc.mu.Unlock()
	if !strings.Contains(sent, "a red fox in the snow") {
		t.Errorf("ComfyUI got %s", sent)
	}
	// Again, with the same settings and seed.
	seed := a.create.items[0].params.Seed
	a.again(s, a.create.items[0])
	pump(t, posted, func() bool { return a.create.jobs[s.ID] == nil })
	if len(a.create.items) < 2 || a.create.items[0].params.Seed != seed {
		t.Errorf("again made %d results, seed %d for %d", len(a.create.items), a.create.items[0].params.Seed, seed)
	}
}

// Stop interrupts ComfyUI, and the run ends without an error.
func TestCreateStops(t *testing.T) {
	fc := newFakeComfy(t, true)
	a, s, f, posted := createTestApp(t, fc)
	tt := ui.NewTester(a.view, 1120, 1600)
	tt.Frame()
	a.create.form(f).prompt = "a lighthouse"
	if err := tt.Click("Generate"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fc.interrupted:
	case <-time.After(10 * time.Second):
		t.Fatal("ComfyUI wasn't interrupted")
	}
	pump(t, posted, func() bool { return a.create.jobs[s.ID] == nil })
	tt.Frame()
	if e := a.create.errs[s.ID]; e != "" {
		t.Errorf("stopping showed %q", e)
	}
	if !tt.HasText("Generate") {
		t.Errorf("texts %q", tt.Texts())
	}
}

// With no ComfyUI runtime, the page leads to deploying one.
func TestCreateEmptyDeploys(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.page = pageCreate
	tt := ui.NewTester(a.view, 390, 844)
	if err := tt.Click("Deploy an image/video model"); err != nil {
		t.Fatal(err)
	}
	if a.page != pageDeploy || a.deploy.form.Plugin != comfyPlugin {
		t.Errorf("page %s, plugin %s", a.page, a.deploy.form.Plugin)
	}
}

// When llama.cpp refused an image model, Deploy offers to deploy it with
// ComfyUI, with the model filled in.
func TestDeploySuggestsComfy(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	s := &core.Session{Phase: core.PhaseFailed, StopReason: "failed"}
	s.ID, s.Account, s.Plugin = "g7", "ada@example.com", "llama"
	s.Values = map[string]string{"repo": "city96/FLUX.1-dev-gguf"}
	a.m.Sessions = append(a.m.Sessions, s)
	a.deploy.launched = s.ID
	tt := ui.NewTester(a.view, 1120, 2000)
	// A family the comfyui plugin doesn't know isn't offered.
	s.Err = "city96/odd-gguf: hf: llama.cpp can't serve this model: it is a diffusion model, for ComfyUI; the built-in comfyui plugin doesn't know its family, but the ComfyUI community task can run it"
	tt.Frame()
	if tt.HasText("Deploy as an image/video model") {
		t.Error("offered ComfyUI for a family it doesn't know")
	}
	s.Err = "city96/FLUX.1-dev-gguf: hf: llama.cpp can't serve this model: it is a diffusion model, for ComfyUI. It is a FLUX.1 [dev] model: use the comfyui plugin to deploy it"
	tt.Frame()
	if err := tt.Click("Deploy as an image/video model"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.deploy.form.Plugin != comfyPlugin {
		t.Fatalf("plugin %s", a.deploy.form.Plugin)
	}
	if repo := a.deploy.values(plugins.Get(comfyPlugin))[comfyRepo]; repo != "city96/FLUX.1-dev-gguf" {
		t.Errorf("the model is %q", repo)
	}
	if !tt.HasText(i18n.T("create.notice_title")) {
		t.Error("no notice about generating on Colab")
	}
}

// Results past createKeep go, oldest first, with their files.
func TestCreateKeepsLast(t *testing.T) {
	var cr createState
	cr.init()
	cr.dir = t.TempDir()
	var first *creation
	for i := 0; i < createKeep+5; i++ {
		it, err := newCreation(comfy.Output{Filename: "clip.mp4", Kind: kindVideo, MIME: "video/mp4", Data: []byte("not really")}, 0, cr.dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = it
		}
		cr.add(it)
	}
	if len(cr.items) != createKeep {
		t.Errorf("%d results", len(cr.items))
	}
	if _, err := os.Stat(first.path); !os.IsNotExist(err) {
		t.Errorf("the oldest file stayed: %v", err)
	}
	cr.cleanup()
	if _, err := os.Stat(cr.dir); !os.IsNotExist(err) {
		t.Errorf("the directory stayed: %v", err)
	}
}

// ComfyUI's errors are explained.
func TestExplainComfyError(t *testing.T) {
	cases := map[string]string{
		"comfy: torch.OutOfMemoryError: CUDA out of memory. Tried to allocate 2.00 GiB":  "err.comfy_oom",
		"comfy: Cannot execute because node WanVideoSampler does not exist.":             "err.comfy_node",
		"comfy: prompt_outputs_failed_validation: Value not in list: unet_name":          "err.comfy_model",
		`comfy: Post "http://127.0.0.1:8188/prompt": dial tcp 127.0.0.1:8188: connectex`: "err.comfy_conn",
	}
	for msg, want := range cases {
		if got, _, ok := explainError(msg); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", msg, got, ok, want)
		}
	}
}
