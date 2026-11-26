package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/cloudflare"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

// webSession adds a running session serving a web page on 7860.
func webSession(a *App, id string) *core.Session {
	s := &core.Session{Phase: core.PhaseRunning}
	s.ID, s.Account, s.Plugin = id, "ada@example.com", "webtest"
	s.Created = time.Now().Add(-5 * time.Minute)
	s.Result = &plugins.Result{Model: "Web UI"}
	s.Forwards = []*core.ForwardState{{Port: plugins.Port{Name: "ui", Remote: 7860, Local: 7860, Kind: plugins.PortWeb}}}
	a.m.Sessions = append(a.m.Sessions, s)
	return s
}

// domainViews are the states of the domains that TestPagesBuild builds,
// and a text each must show.
func domainViews(t *testing.T, a *App) []struct {
	name  string
	setup func()
	want  func() string
} {
	type view = struct {
		name  string
		setup func()
		want  func() string
	}
	return []view{
		{"list", func() {
			a.page = pageSettings
			a.domains.draft = nil
		}, func() string { return "Studio" }},
		{"token-editor", func() {
			a.page = pageSettings
			d, _ := a.m.Domain("home")
			a.editDomain(d, false)
		}, func() string { return i18n.T("domain.token_steps") }},
		{"api-editor", func() {
			a.page = pageSettings
			d, _ := a.m.Domain("studio")
			a.editDomain(d, false)
			a.domains.verified = i18n.F("domain.verified", 1)
		}, func() string { return i18n.F("domain.preview", "https://gpu.example.com") }},
		{"bound", func() {
			a.domains.draft = nil
			a.page = pageRuntimes
		}, func() string { return i18n.T("domain.protected") }},
		{"picker", func() {
			a.page = pageRuntimes
			s := a.m.Session("w9")
			if s == nil {
				s = webSession(a, "w9")
			}
			a.domains.bindOpen, a.domains.bindSession, a.domains.bindPort, a.domains.bindName, a.domains.bindConfig = true, s.ID, 7860, "ui", "home"
		}, func() string { return i18n.T("domain.gpu_warning") }},
	}
}

// A port goes on a domain only after its picker, the check that
// Cloudflare points at it, and the confirmation; once on, it shows its
// address and disconnects.
func TestDomainPublishAsks(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.m.Post = func(func()) {}
	a.page = pageRuntimes
	s := a.m.Session("a1")
	tt := ui.NewTester(a.view, 1120, 4000)
	tt.Frame()

	// Bound: the address, behind Access, and Disconnect.
	if !tt.HasText("https://gpu.example.com") || !tt.HasText("Protected by Cloudflare Access") {
		t.Fatalf("bound state: %q", tt.Texts())
	}
	click(t, tt, "Disconnect")
	if s.HasDomainLink() {
		t.Fatal("Disconnect left the link")
	}

	click(t, tt, "Publish to my domain")
	if !a.domains.bindOpen || a.domains.bindConfig != "home" || !tt.HasText(fmt.Sprintf("In Cloudflare, ai.example.com must point to http://localhost:%d.", plugins.LlamaPort)) {
		t.Fatalf("picker: %+v %q", a.domains, tt.Texts())
	}
	// A tunnel token: not before the user says Cloudflare points at the
	// port.
	tt.Click("Publish")
	tt.Frame()
	if a.domains.bindConfirm {
		t.Fatal("asked before the user said Cloudflare points at the port")
	}
	click(t, tt, fmt.Sprintf("ai.example.com points to http://localhost:%d in Cloudflare", plugins.LlamaPort))
	click(t, tt, "Publish")
	if !a.domains.bindConfirm || !tt.HasText("Publish llama-server on ai.example.com?") {
		t.Fatalf("no confirmation: %q", tt.Texts())
	}
	click(t, tt, "Cancel")
	if s.HasDomainLink() {
		t.Fatal("cancelled, yet published")
	}
	click(t, tt, "Publish to my domain")
	click(t, tt, fmt.Sprintf("ai.example.com points to http://localhost:%d in Cloudflare", plugins.LlamaPort))
	click(t, tt, "Publish")
	click(t, tt, "Publish it")
	l := s.DomainLink(plugins.LlamaPort)
	if !l.Live() || l.Config != "home" || l.Hostname != "ai.example.com" {
		t.Fatalf("confirmed, yet %+v", l)
	}
	if !tt.HasText("Publishing on ai.example.com…") {
		t.Errorf("no progress: %q", tt.Texts())
	}

	// Without an API key, the API can't go on a domain.
	s.Domains = nil
	s.Result.APIKey = ""
	tt.Frame()
	tt.Click("Publish to my domain")
	tt.Frame()
	if a.domains.bindOpen {
		t.Error("opened the picker for an API without a key")
	}
}

// Publishing a domain that serves another runtime says so, warns that a
// web page without Access lets anyone use the GPU, and moves it.
func TestDomainRebind(t *testing.T) {
	i18n.SetLang("en")
	if err := plugins.Register(webTask{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plugins.Unregister("webtest") })
	a := newTestApp(t)
	a.m.Post = func(func()) {}
	a.page = pageRuntimes
	a1 := a.m.Session("a1")
	a1.Domains = map[int]*core.DomainLink{plugins.LlamaPort: {Config: "home", Remote: plugins.LlamaPort, Hostname: "ai.example.com", URL: "https://ai.example.com"}}
	w := webSession(a, "w9")
	tt := ui.NewTester(a.view, 1120, 5000)
	tt.Frame()
	if !tt.HasText("Public on the internet") {
		t.Error("an unprotected domain doesn't say it's public")
	}
	click(t, tt, "Publish to my domain")
	if a.domains.bindSession != w.ID {
		t.Fatalf("picker for %q", a.domains.bindSession)
	}
	warn := "Without Cloudflare Access, anyone who finds this address can use your GPU through it."
	if !tt.HasText(warn) || !tt.HasText(fmt.Sprintf("Home lab serves Qwen3-32B :%d now: publishing here disconnects it there.", plugins.LlamaPort)) {
		t.Fatalf("no warnings: %q", tt.Texts())
	}
	click(t, tt, "ai.example.com points to http://localhost:7860 in Cloudflare")
	click(t, tt, "Publish")
	found := false
	for _, s := range tt.Texts() {
		found = found || strings.Contains(s, warn)
	}
	if !found {
		t.Errorf("the confirmation doesn't warn: %q", tt.Texts())
	}
	click(t, tt, "Publish it")
	if a1.HasDomainLink() || !w.DomainLink(7860).Live() {
		t.Errorf("not moved: a1 %v, w9 %+v", a1.Domains, w.DomainLink(7860))
	}
}

// fakeCloudflare answers verifying a token and listing accounts and zones.
func fakeCloudflare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any
		switch strings.TrimPrefix(r.URL.Path, "/client/v4") {
		case "/user/tokens/verify":
			result = map[string]string{"id": "t", "status": "active"}
		case "/accounts":
			result = []map[string]string{{"id": "acc9", "name": "Lab"}}
		case "/zones":
			result = []map[string]any{{"id": "zone9", "name": "lab.dev", "account": map[string]string{"id": r.URL.Query().Get("account.id")}}}
		default:
			t.Errorf("unexpected %s", r.URL)
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
	}))
	t.Cleanup(srv.Close)
	old := cloudflare.DefaultBase
	cloudflare.DefaultBase = srv.URL + "/client/v4"
	t.Cleanup(func() { cloudflare.DefaultBase = old })
}

// Adding domains in Settings: one with a tunnel token, one with an API
// token verified to choose the account and domain, behind Access; tokens
// stay out of settings.json.
func TestDomainSettings(t *testing.T) {
	i18n.SetLang("en")
	fakeCloudflare(t)
	a := newTestApp(t)
	posted := make(chan func(), 64)
	a.m.Post = func(fn func()) { posted <- fn }
	a.page = pageSettings
	a.m.Settings.Risky.Domains = nil
	tt := ui.NewTester(a.view, 1120, 8000)
	tt.Frame()

	click(t, tt, "Add a domain")
	s := &a.domains
	if s.draft == nil || !tt.HasText("New domain") {
		t.Fatal("no editor")
	}
	tt.Click("Save domain")
	tt.Frame()
	if len(a.m.Settings.Risky.Domains) != 0 {
		t.Fatal("saved an empty domain")
	}
	s.draft.Name, s.draft.Hostname, s.draft.TunnelToken = "Lab", "ai.lab.dev", "tunnel-for-test"
	tt.Frame()
	click(t, tt, "Save domain")
	ds := a.m.Settings.Risky.Domains
	if len(ds) != 1 || ds[0].Host() != "ai.lab.dev" || ds[0].TunnelToken != "tunnel-for-test" || s.draft != nil {
		t.Fatalf("saved %+v", ds)
	}

	click(t, tt, "Add a domain")
	click(t, tt, "API token")
	if s.draft.Mode != core.DomainAPI {
		t.Fatalf("mode %q", s.draft.Mode)
	}
	s.draft.Name, s.draft.APIToken = "API", "api-for-test"
	tt.Frame()
	click(t, tt, "Verify token")
	deadline := time.After(5 * time.Second)
	for s.draft.ZoneID == "" {
		select {
		case fn := <-posted:
			fn()
		case <-deadline:
			t.Fatalf("verifying didn't finish: %+v", s)
		}
	}
	tt.Frame()
	if s.draft.AccountID != "acc9" || s.draft.ZoneName != "lab.dev" || !tt.HasText("The token works: 1 account(s).") {
		t.Fatalf("draft %+v", *s.draft)
	}
	s.draft.Subdomain = "gpu"
	tt.Frame()
	click(t, tt, "Protect with Cloudflare Access (only these emails can sign in)")
	tt.Frame()
	if !tt.HasText("Add at least one email for Cloudflare Access.") {
		t.Error("Access without emails isn't flagged")
	}
	s.emails = strings.Join([]string{"ada", "bob"}, "@lab.dev, ") + "@lab.dev"
	tt.Frame()
	click(t, tt, "Save domain")
	ds = a.m.Settings.Risky.Domains
	if len(ds) != 2 || ds[1].Host() != "gpu.lab.dev" || !ds[1].Protected() || len(ds[1].AccessEmails) != 2 {
		t.Fatalf("saved %+v", ds)
	}
	b, _ := os.ReadFile(filepath.Join(a.m.Dir, "settings.json"))
	if strings.Contains(string(b), "tunnel-for-test") || strings.Contains(string(b), "api-for-test") || !strings.Contains(string(b), "gpu") {
		t.Errorf("settings.json: %s", b)
	}

	// Deleting asks first; deleting what it made on Cloudflare asks too.
	tt.Frame()
	click(t, tt, "Delete the tunnel and DNS record on Cloudflare API")
	if !s.confirmRemove || !tt.HasText("Delete on Cloudflare?") {
		t.Fatal("removing on Cloudflare didn't ask")
	}
	click(t, tt, "Cancel")
	click(t, tt, "Delete Lab")
	click(t, tt, "Delete")
	if len(a.m.Settings.Risky.Domains) != 1 {
		t.Errorf("not deleted: %+v", a.m.Settings.Risky.Domains)
	}

	// With public links off, there's nothing to set.
	a.m.Settings.Risky.PublicLinks = false
	tt.Frame()
	if tt.HasText("Add a domain") || !tt.HasText(i18n.T("domain.needs_public")) {
		t.Error("domains offered with public links off")
	}
}
