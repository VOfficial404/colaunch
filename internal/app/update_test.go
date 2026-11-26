package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/update"
)

func newUpdate(latest string) update.Info {
	return update.Info{
		Current: "0.1.7", Latest: latest, Available: true, CheckedAt: time.Now(),
		Notes:    "## Fixes\n\n- Downloads resume **after** a dropped connection\n- [Docs](https://github.com/VOfficial404/colaunch)\n\n```sh\ncolaunch --version\n```",
		URL:      "https://github.com/VOfficial404/colaunch/releases/tag/v" + latest,
		Download: "https://github.com/VOfficial404/colaunch/releases/download/v" + latest + "/colaunch-" + latest + ".apk",
	}
}

// updateViews are the update's notice, card and dialog, on each platform.
func updateViews(a *App, phone bool) []struct {
	name  string
	setup func()
	want  func() string
} {
	type view = struct {
		name  string
		setup func()
		want  func() string
	}
	offer := func() {
		a.update.info, a.update.has, a.update.skipped, a.update.open = newUpdate("0.2.0"), true, "", false
	}
	dialog := func(goos string, native bool) func() {
		return func() {
			offer()
			a.update.info.Native, a.update.goos, a.update.open = native, goos, true
			a.page = pageDeploy
		}
	}
	notice := func() string { return i18n.F("update.available", "0.2.0") }
	if phone {
		notice = func() string { return i18n.T("update.card_title") }
	}
	return []view{
		{"notice", func() { offer(); a.page = pageDeploy }, func() string {
			if phone {
				return i18n.T("nav.settings")
			}
			return notice()
		}},
		{"settings", func() { offer(); a.page = pageSettings }, notice},
		{"dialog-desktop", dialog("windows", true), func() string { return i18n.T("update.install") }},
		{"dialog-manual", dialog("linux", false), func() string { return i18n.T("update.manual_help") }},
		{"dialog-android", dialog("android", false), func() string { return i18n.T("update.download_apk") }},
		{"dialog-ios", dialog("ios", false), func() string { return i18n.T("update.ios_help") }},
	}
}

// A skipped version is not offered again, but the next one is.
func TestSkipUpdate(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	path := filepath.Join(t.TempDir(), "update.json")
	a.update.checker = update.New(update.Config{Current: "0.1.7", Path: path})
	t.Cleanup(func() { stopUpdates(a) })
	tt := ui.NewTester(a.view, 1120, 780)
	a.updateResult(newUpdate("0.2.0"), nil)
	tt.Frame()
	if err := tt.Click("Version 0.2.0 is available: show the update"); err != nil {
		t.Fatal(err)
	}
	if !a.update.open || !tt.HasText("What's new") {
		t.Fatal("the dialog didn't open")
	}
	if err := tt.Click("Skip this version"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.updateOffered() || tt.HasText("Version 0.2.0 is available") {
		t.Fatal("the skipped version is still offered")
	}
	if c := update.New(update.Config{Current: "0.1.7", Path: path}); c.Skipped() != "0.2.0" {
		t.Fatalf("skipped %q kept", c.Skipped())
	}
	a.updateResult(newUpdate("0.2.0"), nil)
	if a.updateOffered() {
		t.Fatal("the skipped version is offered after another check")
	}
	a.updateResult(newUpdate("0.2.1"), nil)
	tt.Frame()
	if !a.updateOffered() || !tt.HasText("Version 0.2.1 is available") {
		t.Fatal("the next version isn't offered")
	}
	// Later closes the dialog and keeps the notice.
	a.update.open = true
	tt.Frame()
	if err := tt.Click("Later"); err != nil {
		t.Fatal(err)
	}
	if a.update.open || !a.updateOffered() {
		t.Fatal("Later didn't just close the dialog")
	}
}

// Checking from Settings says what it found; automatic checks say nothing.
func TestManualUpdateCheck(t *testing.T) {
	i18n.SetLang("en")
	tag, status := "v0.2.0", http.StatusOK
	var header http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header()[k] = v
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "body": "- New", "html_url": "https://github.com/VOfficial404/colaunch/releases/tag/" + tag})
	}))
	defer srv.Close()
	a := newTestApp(t)
	a.page = pageSettings
	a.update.async = func(fn func()) { fn() }
	a.update.checker = update.New(update.Config{Current: "0.1.7", GOOS: "android", API: srv.URL, Client: srv.Client()})
	t.Cleanup(func() { stopUpdates(a) })
	tt := ui.NewTester(a.view, 1120, 5200)
	check := func(want ...string) {
		t.Helper()
		// Toasts from before go.
		tt.Advance(30 * time.Second)
		if err := tt.Click("Check for updates"); err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !tt.HasText(w) {
				t.Errorf("the toast doesn't say %q", w)
			}
		}
	}

	// The toast's action, which only it has on a desktop.
	check("View update")
	if err := tt.Click("View update"); err != nil {
		t.Fatal(err)
	}
	if !a.update.open || !tt.HasText("What's new") {
		t.Error("the toast's action doesn't open the update")
	}
	if err := tt.Click("Later"); err != nil {
		t.Fatal(err)
	}

	tag = "v0.1.7"
	check("Colaunch is up to date", "You have version 0.1.7, the latest.")

	status = http.StatusInternalServerError
	check("Couldn't check for updates", "GitHub answered with error 500.")

	status = http.StatusForbidden
	header = http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}}
	check("Couldn't check for updates", "GitHub is limiting requests")

	// An automatic check that fails says nothing.
	a.toasts = nil
	a.checkUpdates(false)
	if len(a.toasts) != 0 {
		t.Errorf("an automatic check showed %+v", a.toasts)
	}
}

func stopUpdates(a *App) {
	if a.update.timer != nil {
		a.update.timer.Stop()
	}
}
