package app

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/core"
	"colaunch/internal/hf"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

type mapSecrets map[string][]byte

func (m mapSecrets) Get(k string) ([]byte, error) { return m[k], nil }
func (m mapSecrets) Set(k string, v []byte) error { m[k] = v; return nil }
func (m mapSecrets) Delete(k string) error        { delete(m, k); return nil }

// newTestApp is an app with an account, a model running on an A100 and
// one still deploying, as the pages show them.
func newTestApp(t *testing.T) *App {
	m := core.New(t.TempDir(), mapSecrets{})
	acct := &core.Account{}
	acct.Email, acct.Name = "ada@example.com", "Ada"
	acct.Quota = &colab.Quota{Tier: "PRO", Balance: 87.5, RateHourly: 11.77, FetchedAt: time.Now()}
	acct.Specs = []colab.RuntimeSpec{
		{Key: colab.Spec{Variant: colab.GPU, Accelerator: "T4"}, Eligible: true},
		{Key: colab.Spec{Variant: colab.GPU, Accelerator: "L4"}, Eligible: true},
		{Key: colab.Spec{Variant: colab.GPU, Accelerator: "A100"}, Eligible: true},
		{Key: colab.Spec{Variant: colab.GPU, Accelerator: "H100"}, Eligible: false},
		{Key: colab.Spec{Variant: colab.TPU, Accelerator: "V6E1"}, Eligible: true},
	}
	m.Accounts = []*core.Account{acct}
	m.Settings.ActiveAccount = acct.Email

	run := &core.Session{Phase: core.PhaseRunning}
	run.ID, run.Account, run.Plugin = "a1", acct.Email, "llama"
	run.Spec = colab.Spec{Variant: colab.GPU, Accelerator: "A100", Shape: colab.HighMem}
	run.Created, run.StartBalance = time.Now().Add(-47*time.Minute), 96.7
	run.GPUName, run.VRAM = "NVIDIA A100-SXM4-80GB", 81920
	run.Guards = core.Guards{StopAfter: 240, IdleStop: 30, SpendCap: 20}
	run.Result = &plugins.Result{Model: "Qwen3-32B", APIKey: "sk-colaunch-test", Info: [][2]string{{"quant", "Q8_0"}, {"size", "34.8 GB"}, {"context", "16384"}}}
	// Its API is public, as the risky features allow.
	run.Forwards = []*core.ForwardState{{Port: plugins.Port{Name: "llama-server", Remote: plugins.LlamaPort, Local: 8080, Kind: plugins.PortOpenAI, Path: "/"}}}
	run.Public = map[int]*core.PublicLink{plugins.LlamaPort: {Remote: plugins.LlamaPort, URL: "https://seasonal-deck-organisms-sf.trycloudflare.com"}}
	m.Settings.Risky.PublicLinks = true
	// Two of the user's own domains, the second, behind Cloudflare
	// Access, serving the API too.
	m.Settings.Risky.Domains = []core.DomainConfig{
		{ID: "home", Name: "Home lab", Mode: core.DomainToken, Hostname: "ai.example.com", TunnelToken: "test-tunnel"},
		{ID: "studio", Name: "Studio", Mode: core.DomainAPI, APIToken: "test-api", AccountID: "acc1", AccountName: "Ada's account",
			ZoneID: "zone1", ZoneName: "example.com", Subdomain: "gpu", Access: true, AccessEmails: []string{"ada@example.com"}},
	}
	run.Domains = map[int]*core.DomainLink{plugins.LlamaPort: {Config: "studio", Remote: plugins.LlamaPort, Hostname: "gpu.example.com", URL: "https://gpu.example.com", Protected: true}}
	st := &agent.Status{CPUs: 12}
	st.GPUs = append(st.GPUs, struct {
		Name        string `json:"name"`
		MemoryTotal int64  `json:"memory_total_mb"`
		MemoryUsed  int64  `json:"memory_used_mb"`
		Utilization int    `json:"utilization"`
	}{"NVIDIA A100-SXM4-80GB", 81920, 41200, 63})
	st.Memory.Total, st.Memory.Available = 167<<30, 120<<30
	st.Disk.Total, st.Disk.Free = 235<<30, 160<<30
	st.Watchdog.LastActivity = float64(time.Now().Add(-3 * time.Minute).Unix())
	run.Status = st
	run.Log = []core.LogLine{{Time: time.Now(), Text: "✓ Ready."}}

	dep := &core.Session{Phase: core.PhaseDeploying, PluginPhase: "download"}
	dep.ID, dep.Account, dep.Plugin = "b2", acct.Email, "llama"
	dep.Spec = colab.Spec{Variant: colab.GPU, Accelerator: "T4"}
	dep.Created = time.Now().Add(-4 * time.Minute)
	dep.Values = map[string]string{"repo": "unsloth/gemma-3-12b-it-GGUF"}
	dep.Log = []core.LogLine{
		{Time: time.Now(), Text: "unsloth/gemma-3-12b-it-GGUF: Q6_K, 9.7 GB (GPU 15.0 GB)"},
		{Time: time.Now(), Text: "Downloading gemma-3-12b-it-Q6_K.gguf"},
	}
	wait := &core.Session{Phase: core.PhaseWaiting, Grabbing: true, Round: 14, Attempt: 41}
	wait.ID, wait.Account, wait.Plugin = "c3", acct.Email, "llama"
	wait.Spec = colab.Spec{Variant: colab.GPU, Accelerator: "H100"}
	wait.Created, wait.NextTry = time.Now().Add(-23*time.Minute), time.Now().Add(18*time.Second)
	wait.Values = map[string]string{"repo": "unsloth/Llama-3.3-70B-Instruct-GGUF"}
	wait.TryErr = "colab: no such accelerator is available now: L4"
	failed := &core.Session{Phase: core.PhaseFailed, StopReason: "failed"}
	failed.ID, failed.Account, failed.Plugin = "d4", acct.Email, "llama"
	failed.Spec = colab.Spec{Variant: colab.GPU, Accelerator: "L4"}
	failed.Created, failed.StoppedAt = time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour+time.Minute)
	failed.Values = map[string]string{"repo": "unsloth/Qwen3-8B-GGUF"}
	failed.Err = "colab: the account has too many runtimes: stop one of its runtimes first"
	m.Sessions = []*core.Session{failed, run, dep, wait}
	a := &App{m: m, page: pageDeploy}
	a.deploy.init(m)
	a.runtimes.remember(wait.ID, core.Machine{Variant: "gpu", Accelerator: "H100", Fallbacks: []string{"A100", "L4"}, Grab: true, RetrySeconds: 30, MaxWait: 120})
	a.deploy.form.Grab, a.deploy.form.Fallbacks = true, []string{"L4", "T4"}
	a.deploy.form.Accelerator = "A100"
	return a
}

var sizes = []struct {
	name string
	w, h int
}{{"desktop", 1120, 780}, {"phone", 390, 844}}

// Every page builds at a desktop's size and a phone's, in each language,
// without the view panicking or showing a key in place of words.
func TestPagesBuild(t *testing.T) {
	shots := os.Getenv("COLAUNCH_SHOTS")
	for _, lang := range i18n.Languages {
		i18n.SetLang(lang)
		for _, size := range sizes {
			for _, dark := range []bool{false, true} {
				a := newTestApp(t)
				tt := ui.NewTester(a.view, size.w, size.h)
				tt.SetDark(dark)
				for _, it := range navItems {
					a.page = it.page
					tt.Frame()
					for _, s := range tt.Texts() {
						if i18n.Has(s) {
							t.Errorf("%s %s %s shows the key %q", lang, size.name, it.page, s)
						}
					}
					if shots != "" && lang != "zh-Hant" {
						theme := "light"
						if dark {
							theme = "dark"
						}
						save(t, tt, filepath.Join(shots, fmt.Sprintf("%s-%s-%s-%s.png", it.page, size.name, theme, lang)))
					}
				}
				// The Tasks page's other views.
				for name, setup := range taskViews(t, a) {
					a.page = pagePlugins
					setup()
					tt.Frame()
					for _, s := range tt.Texts() {
						if i18n.Has(s) {
							t.Errorf("%s %s tasks %s shows the key %q", lang, size.name, name, s)
						}
					}
					if shots != "" && lang != "zh-Hant" && !dark {
						save(t, tt, filepath.Join(shots, fmt.Sprintf("tasks-%s-%s-%s.png", name, size.name, lang)))
					}
				}
				// The Create page's states: none to run on, the form, a
				// run under way and results.
				for _, v := range createViews(t, a) {
					v.setup()
					tt.Frame()
					for _, s := range tt.Texts() {
						if i18n.Has(s) {
							t.Errorf("%s %s create %s shows the key %q", lang, size.name, v.name, s)
						}
					}
					if shots != "" && lang != "zh-Hant" {
						theme := "light"
						if dark {
							theme = "dark"
						}
						save(t, tt, filepath.Join(shots, fmt.Sprintf("create-%s-%s-%s-%s.png", v.name, size.name, theme, lang)))
					}
				}
				// Settings' domains and the runtime card's domain picker,
				// in a window tall enough to show them whole.
				tt.SetSize(size.w, 5200)
				for _, v := range domainViews(t, a) {
					v.setup()
					tt.Frame()
					for _, s := range tt.Texts() {
						if i18n.Has(s) {
							t.Errorf("%s %s domain %s shows the key %q", lang, size.name, v.name, s)
						}
					}
					if !tt.HasText(v.want()) {
						t.Errorf("%s %s domain %s doesn't show %q", lang, size.name, v.name, v.want())
					}
					if shots != "" && lang != "zh-Hant" && !dark {
						save(t, tt, filepath.Join(shots, fmt.Sprintf("domain-%s-%s-%s.png", v.name, size.name, lang)))
					}
				}
				tt.SetSize(size.w, size.h)
				// An update: the sidebar's notice or the tab's dot, Settings'
				// card, and the dialog on each platform.
				for _, v := range updateViews(a, size.name == "phone") {
					v.setup()
					tt.Frame()
					for _, s := range tt.Texts() {
						if i18n.Has(s) {
							t.Errorf("%s %s update %s shows the key %q", lang, size.name, v.name, s)
						}
					}
					if !tt.HasText(v.want()) {
						t.Errorf("%s %s update %s doesn't show %q", lang, size.name, v.name, v.want())
					}
					if shots != "" && lang != "zh-Hant" && !dark {
						save(t, tt, filepath.Join(shots, fmt.Sprintf("update-%s-%s-%s.png", v.name, size.name, lang)))
					}
				}
				a.update.open, a.update.has = false, false
			}
		}
	}
	i18n.SetLang("en")
}

// The sidebar's items take the user to their pages.
func TestSidebarNavigates(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1120, 780)
	if err := tt.Click("Accounts"); err != nil {
		t.Fatal(err)
	}
	if a.page != pageAccounts || !tt.HasText("ada@example.com") {
		t.Errorf("page %s after clicking Accounts", a.page)
	}
}

// On a phone the bar of tabs takes the user to the runtimes.
func TestTabsNavigate(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 390, 844)
	if err := tt.Click("Runtimes"); err != nil {
		t.Fatal(err)
	}
	if a.page != pageRuntimes || !tt.HasText("Qwen3-32B") {
		t.Errorf("page %s, texts %q", a.page, tt.Texts())
	}
}

// Switching the machine to TPU offers the TPUs the account may use.
func TestHardwareFollowsAccount(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1120, 1600)
	if err := tt.Click("TPU"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.deploy.form.Accelerator != "V6E1" {
		t.Errorf("accelerator %q for TPU", a.deploy.form.Accelerator)
	}
}

// The errors people meet are explained; others show as they are.
func TestExplainError(t *testing.T) {
	cases := map[string]string{
		colab.ErrUnavailable.Error() + ": L4":                    "err.unavailable",
		colab.ErrQuota.Error():                                   "err.quota",
		colab.ErrTooManyRuntimes.Error() + ": stop one":          "err.too_many",
		colab.ErrDenylisted.Error():                              "err.denylisted",
		colab.ErrUnauthorized.Error():                            "err.signed_out",
		"google: the account's sign-in expired or was revoked":   "err.signed_out",
		"google: no OAuth client is configured (Settings → ...)": "err.no_client",
		"colab: the output of command 18 has no start":           "err.terminal_output",
		"opening a terminal to wait on: context deadline exceeded\ncolab: the terminal command didn't start": "err.monitor_connection",
		"invalid readiness probe response":                                   "err.terminal_output",
		"the readiness probe didn't answer":                                  "err.terminal_output",
		"hf: no such repository, or it needs an access token":                "err.hf_not_found",
		"gave up after 3 tries in 1m: " + colab.ErrQuota.Error():             "err.quota",
		"hf: no quantization of the model fits in this machine's":            "err.too_big",
		"a/b: " + hf.ErrOnlySafetensors.Error() + " (46 .safetensors files)": "err.only_safetensors",
		"a/b: " + hf.ErrOnlyProjector.Error():                                "err.only_projector",
		"a/b: " + hf.ErrUnnamedGGUF.Error():                                  "err.unnamed_gguf",
		"a/b: " + hf.ErrNoGGUF.Error():                                       "err.no_gguf",
	}
	for msg, want := range cases {
		if got, next, ok := explainError(msg); !ok || got != want || next != want+"_next" {
			t.Errorf("%q: %q %q %v, want %q", msg, got, next, ok, want)
		}
	}
	if _, _, ok := explainError("exit status 1"); ok {
		t.Error("an unknown error was explained")
	}
}

// Without an OAuth client, the guide's first step is the one to do, and
// adding an account does nothing.
func TestOnboardingNeedsClient(t *testing.T) {
	i18n.SetLang("en")
	m := core.New(t.TempDir(), mapSecrets{})
	if m.Client().Valid() {
		t.Skip("this build has an OAuth client")
	}
	a := &App{m: m, page: pageAccounts}
	a.deploy.init(m)
	tt := ui.NewTester(a.view, 1120, 780)
	tt.Frame()
	if a.onboardingStep() != 0 || !tt.HasText("Connect an OAuth client") {
		t.Fatalf("step %d, texts %q", a.onboardingStep(), tt.Texts())
	}
	tt.Click("Add account")
	tt.Frame()
	if a.accounts.login != nil || a.accounts.err != "" {
		t.Errorf("Add account tried to sign in: %q", a.accounts.err)
	}
	if shots := os.Getenv("COLAUNCH_SHOTS"); shots != "" {
		for _, lang := range []string{"en", "zh-Hans"} {
			i18n.SetLang(lang)
			for _, size := range sizes {
				for _, page := range []string{pageAccounts, pageDeploy} {
					a.page = page
					st := ui.NewTester(a.view, size.w, size.h)
					st.Frame()
					save(t, st, filepath.Join(shots, fmt.Sprintf("onboarding-%s-%s-%s.png", page, size.name, lang)))
				}
			}
		}
		i18n.SetLang("en")
	}
}

// Stopping a runtime still grabbing asks to stop grabbing, not to give a
// runtime back.
func TestStopGrabbingAsks(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.page = pageRuntimes
	tt := ui.NewTester(a.view, 1120, 2400)
	tt.Frame()
	if err := tt.Click("Stop grabbing"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.runtimes.toStop == nil || a.runtimes.toStop.ID != "c3" || !tt.HasText("Stop grabbing?") {
		t.Fatalf("dialog for %v, texts %q", a.runtimes.toStop, tt.Texts())
	}
	if shots := os.Getenv("COLAUNCH_SHOTS"); shots != "" {
		save(t, tt, filepath.Join(shots, "stop-grab-dialog.png"))
	}
}

// Runtimes show running first, then starting, then waiting for one.
func TestLiveOrder(t *testing.T) {
	a := newTestApp(t)
	live := a.m.Live()
	sortLive(live)
	var ids []string
	for _, s := range live {
		ids = append(ids, s.ID)
	}
	if fmt.Sprint(ids) != "[a1 b2 c3]" {
		t.Errorf("order %v", ids)
	}
}

// A click on a switch's label flips it.
func TestSwitchLabelToggles(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1120, 2400)
	tt.Frame()
	before := a.deploy.form.HighMem
	if err := tt.Click("High-RAM"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.deploy.form.HighMem == before {
		t.Error("clicking High-RAM didn't flip it")
	}
}

// Launching without a required field says so below the field, and starts
// nothing.
func TestLaunchNeedsRequired(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.deploy.values(plugins.Get("llama"))["repo"] = ""
	n := len(a.m.Sessions)
	tt := ui.NewTester(a.view, 1120, 780)
	tt.Frame()
	if err := tt.Click("Launch"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.m.Sessions) != n || a.deploy.fieldErr["llama.repo"] == "" || !tt.HasText("Required") {
		t.Errorf("sessions %d, field errors %v", len(a.m.Sessions), a.deploy.fieldErr)
	}
}

// The log is written out again only when it has new lines.
func TestLogCached(t *testing.T) {
	a := newTestApp(t)
	s := a.m.Session("b2")
	tt := ui.NewTester(func(c *ui.Context) { a.logBox(c, s) }, 600, 400)
	tt.Frame()
	first := a.runtimes.logs[s.ID]
	tt.Frame()
	if a.runtimes.logs[s.ID] != first {
		t.Error("the log was written out again without new lines")
	}
	s.Log = append(s.Log, core.LogLine{Time: time.Now(), Text: "more"})
	tt.Frame()
	if a.runtimes.logs[s.ID] == first || a.runtimes.logs[s.ID].n != 3 {
		t.Error("the log didn't take its new line")
	}
}

func save(t *testing.T, tt *ui.Tester, path string) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, tt.Image())
}

// TestIcon draws the app's icon into resources/icon.png when
// COLAUNCH_ICON is set: go test ./internal/app -run TestIcon.
func TestIcon(t *testing.T) {
	if os.Getenv("COLAUNCH_ICON") == "" {
		t.Skip("set COLAUNCH_ICON to draw the icon")
	}
	view := func(c *ui.Context) {
		th := *ui.LightTheme()
		th.Background = ui.Transparent
		c.SetTheme(&th)
		ui.Column(c).Fill().Center().Children(func() {
			ui.Column(c).Size(824, 824).Radius(185).Center().
				Gradient(ui.Hex("#fbbf24"), ui.Hex("#e8710a"), 135).
				Children(func() {
					ui.Icon(c, iconRocket).Size(470, 470).TextColor(ui.Hex("#ffffff"))
				})
		})
	}
	tt := ui.NewTester(view, 1024, 1024)
	tt.Frame()
	// Clear what's outside the rounded square, which the window painted.
	img := tt.Image()
	const lo, hi, r = 100.0, 924.0, 185.0
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			cx := math.Max(lo+r, math.Min(hi-r, px))
			cy := math.Max(lo+r, math.Min(hi-r, py))
			d := math.Hypot(px-cx, py-cy) - r
			if px < lo || px > hi || py < lo || py > hi {
				d = math.Max(d, 1)
			}
			a := math.Max(0, math.Min(1, 0.5-d))
			i := img.PixOffset(x, y)
			for k := 0; k < 4; k++ {
				img.Pix[i+k] = uint8(float64(img.Pix[i+k]) * a)
			}
		}
	}
	out, err := os.Create("../../resources/icon.png")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	png.Encode(out, img)
}
