package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/colab"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

// webTask is a task used through a web page, which stops at Validate.
type webTask struct{}

func (webTask) ID() string                { return "webtest" }
func (webTask) Name() plugins.Text        { return plugins.Text{"en": "Web test"} }
func (webTask) Description() plugins.Text { return plugins.Text{"en": "A web page."} }
func (webTask) Fields() []plugins.Field   { return nil }
func (webTask) Ports() []plugins.Port {
	return []plugins.Port{{Name: "ui", Remote: 7860, Local: 7860, Kind: plugins.PortWeb}}
}
func (webTask) Validate(context.Context, map[string]string, string) (*plugins.Hardware, error) {
	return nil, errors.New("test: stop here")
}
func (webTask) Deploy(context.Context, *plugins.Env) (*plugins.Result, error) { return nil, nil }
func (webTask) Stop(context.Context, *colab.Shell) error                      { return nil }

func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("click %q: %v", label, err)
	}
	tt.Frame()
}

// Turning a risky feature on asks first, and only the confirmation turns it
// on; turning it off asks nothing.
func TestRiskySwitchAsks(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.page = pageSettings
	tt := ui.NewTester(a.view, 1120, 6000)
	tt.Frame()
	set := &a.m.Settings.Risky
	label := "Aggressive grabbing: rounds as often as every 3 seconds"

	click(t, tt, label)
	if set.FastGrab || !a.risky.confirm || !tt.HasText("Turn on aggressive grabbing?") {
		t.Fatalf("fast grab %v, asking %v", set.FastGrab, a.risky.confirm)
	}
	click(t, tt, "Cancel")
	if set.FastGrab || a.risky.confirm {
		t.Fatalf("cancelled, yet fast grab %v", set.FastGrab)
	}

	click(t, tt, label)
	click(t, tt, "I understand the risks, turn it on")
	if !set.FastGrab {
		t.Fatal("confirmed, yet fast grab is off")
	}
	b, _ := os.ReadFile(filepath.Join(a.m.Dir, "settings.json"))
	if !strings.Contains(string(b), `"fast_grab": true`) {
		t.Errorf("settings.json doesn't keep it: %s", b)
	}

	click(t, tt, label)
	if set.FastGrab || a.risky.confirm {
		t.Errorf("turning off: fast grab %v, asking %v", set.FastGrab, a.risky.confirm)
	}

	// Turning public links off closes those open.
	a.m.Post = func(func()) {}
	click(t, tt, "Public links for web pages and APIs")
	if set.PublicLinks || a.risky.confirm || a.m.Session("a1").HasPublicLink() {
		t.Errorf("public links %v, asking %v, a1 public %v", set.PublicLinks, a.risky.confirm, a.m.Session("a1").HasPublicLink())
	}
}

// Each public link asks first; an API without a key can't have one.
func TestPublicLinkAsks(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.m.Post = func(func()) {}
	a.page = pageRuntimes
	s := a.m.Session("a1")
	s.Public = nil
	tt := ui.NewTester(a.view, 1120, 3000)
	tt.Frame()

	click(t, tt, "Make a public link")
	if !a.runtimes.shareConfirm || !tt.HasText("Make llama-server public?") {
		t.Fatalf("asking %v, texts %q", a.runtimes.shareConfirm, tt.Texts())
	}
	click(t, tt, "Cancel")
	if s.HasPublicLink() {
		t.Fatal("cancelled, yet the link started")
	}
	click(t, tt, "Make a public link")
	click(t, tt, "Make it public")
	if !s.PublicLink(plugins.LlamaPort).Live() {
		t.Fatalf("confirmed, yet no link: %+v", s.Public)
	}
	click(t, tt, "Cancel")
	if s.HasPublicLink() {
		t.Error("cancelling the link left it")
	}

	s.Result.APIKey = ""
	tt.Frame()
	if !tt.HasText(i18n.T("share.needs_key")) {
		t.Error("no word on the API key")
	}
	tt.Click("Make a public link")
	tt.Frame()
	if a.runtimes.shareConfirm && a.runtimes.shareSession == s.ID {
		t.Error("asked to make an API without a key public")
	}
}

// On the free tier a web task can't launch until the setting allows it, and
// then each launch asks first.
func TestFreeWebTaskLaunch(t *testing.T) {
	i18n.SetLang("en")
	if err := plugins.Register(webTask{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plugins.Unregister("webtest") })
	a := newTestApp(t)
	a.m.Post = func(func()) {}
	a.m.Active().Quota = &colab.Quota{Tier: "FREE"}
	a.deploy.form.Plugin = "webtest"
	a.deploy.form.Grab = false
	n := len(a.m.Sessions)
	tt := ui.NewTester(a.view, 1120, 1600)
	tt.Frame()
	if !tt.HasText("Web tasks are off on the free tier") {
		t.Fatalf("no notice: %q", tt.Texts())
	}
	tt.Click("Launch")
	tt.Frame()
	if len(a.m.Sessions) != n || a.deploy.freeConfirm {
		t.Fatal("launched a web task on the free tier with the setting off")
	}
	click(t, tt, "Turn on in Settings")
	if a.page != pageSettings {
		t.Errorf("page %s", a.page)
	}

	a.m.Settings.Risky.FreeWebUI = true
	a.page = pageDeploy
	tt.Frame()
	click(t, tt, "Launch")
	if !a.deploy.freeConfirm || len(a.m.Sessions) != n {
		t.Fatalf("asking %v, %d sessions", a.deploy.freeConfirm, len(a.m.Sessions))
	}
	click(t, tt, "Cancel")
	if len(a.m.Sessions) != n {
		t.Fatal("cancelled, yet launched")
	}
	click(t, tt, "Launch")
	click(t, tt, "Launch anyway")
	if len(a.m.Sessions) != n+1 || a.m.Sessions[n].Plugin != "webtest" {
		t.Errorf("confirmed: %d sessions", len(a.m.Sessions))
	}
}

// Fast grabbing lets the interval go under 10 seconds, with a warning.
func TestFastGrabInterval(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.deploy.grabOpen = true
	a.deploy.form.RetrySeconds = 3
	tt := ui.NewTester(a.view, 1120, 2400)
	tt.Frame()
	if a.deploy.form.RetrySeconds != 30 || tt.HasText(i18n.T("grab.too_fast")) {
		t.Errorf("without fast grabbing: %v s", a.deploy.form.RetrySeconds)
	}
	a.m.Settings.Risky.FastGrab = true
	a.deploy.form.RetrySeconds = 3
	tt.Frame()
	if a.deploy.form.RetrySeconds != 3 || !tt.HasText(i18n.T("grab.too_fast")) {
		t.Errorf("with fast grabbing: %v s", a.deploy.form.RetrySeconds)
	}
}
