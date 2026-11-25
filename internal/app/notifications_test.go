package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/notify"
)

// Adding a channel, turning it off, testing it and deleting it, from the
// Notifications section.
func TestNotificationChannels(t *testing.T) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.page = pageSettings
	tt := ui.NewTester(a.view, 1120, 6000)
	if err := tt.Click("Add channel"); err != nil {
		t.Fatal(err)
	}
	s := a.notifState()
	if s.draft == nil || !tt.HasText("New channel") {
		t.Fatal("no editor")
	}
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	if len(a.m.Settings.Notifications.Channels) != 0 {
		t.Fatal("saved a channel without its token")
	}
	hits := make(chan string, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits <- r.URL.Path }))
	defer ts.Close()
	s.draft.Kind, s.draft.Name = notify.KindWebhook, "phone"
	s.draft.Secret["url"] = ts.URL + "/hook"
	tt.Frame()

	// The test notice, with its result as a toast.
	posted := make(chan func(), 1)
	a.m.Post = func(fn func()) { posted <- fn }
	if err := tt.Click("Send test notification"); err != nil {
		t.Fatal(err)
	}
	select {
	case fn := <-posted:
		fn()
	case <-time.After(10 * time.Second):
		t.Fatal("the test didn't finish")
	}
	if p := <-hits; p != "/hook" {
		t.Errorf("the test went to %s", p)
	}
	tt.Frame()
	if !tt.HasText("Test notification sent") {
		t.Error("no toast for the test")
	}

	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	chs := a.m.Settings.Notifications.Channels
	if len(chs) != 1 || chs[0].Value("url") != ts.URL+"/hook" || !chs[0].Enabled || !chs[0].Wants(notify.EventReady) || s.draft != nil {
		t.Fatalf("saved %+v", chs)
	}
	tt.Frame()
	if err := tt.Click("Turn “phone” on or off"); err != nil {
		t.Fatal(err)
	}
	if a.m.Settings.Notifications.Channels[0].Enabled {
		t.Error("the switch didn't turn it off")
	}
	tt.Frame()
	if err := tt.Click("Delete phone"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.m.Settings.Notifications.Channels) != 0 {
		t.Error("the channel stayed")
	}
}

// Why a notice didn't go reads as words, naming the value at fault.
func TestNotifyError(t *testing.T) {
	i18n.SetLang("en")
	_, err := notify.New(notify.Channel{Kind: notify.KindNtfy, Params: map[string]string{"server": "http://ntfy.example.com"}, Secret: map[string]string{"topic": "x"}}, nil)
	if got := notifyError(err); !strings.HasPrefix(got, "Server must use https") {
		t.Errorf("got %q", got)
	}
	if got := notifyError(&notify.Error{Code: "status", Status: 401, Detail: "Unauthorized"}); got != "The service answered HTTP 401: Unauthorized" {
		t.Errorf("got %q", got)
	}
	if got := notifyError(errors.New("x")); got != "x" {
		t.Errorf("got %q", got)
	}
}

// The section, with a channel and the editor open on each kind, builds
// at a desktop's size and a phone's, in each language, without a key in
// place of words.
func TestNotificationsBuild(t *testing.T) {
	shots := os.Getenv("COLAUNCH_SHOTS")
	for _, lang := range i18n.Languages {
		i18n.SetLang(lang)
		for _, size := range sizes {
			a := newTestApp(t)
			a.page = pageSettings
			a.m.Settings.Notifications.Channels = []notify.Channel{{ID: "c1", Kind: notify.KindBark, Name: "iPhone", Enabled: true, Events: []string{"ready", "stopped"}}}
			s := a.notifState()
			tt := ui.NewTester(a.view, size.w, 6000)
			for _, kind := range notify.Kinds {
				s.draft = &notify.Channel{Kind: kind, Events: notify.Events, Params: map[string]string{"x": "1"}, Secret: map[string]string{}}
				s.isNew = true
				tt.Frame()
				for _, txt := range tt.Texts() {
					if i18n.Has(txt) {
						t.Errorf("%s %s %s shows the key %q", lang, size.name, kind, txt)
					}
				}
				if shots != "" && kind == notify.KindTelegram {
					save(t, tt, filepath.Join(shots, fmt.Sprintf("notifications-%s-%s.png", size.name, lang)))
				}
			}
		}
	}
	i18n.SetLang("en")
}
