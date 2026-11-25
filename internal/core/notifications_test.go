package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/notify"
	"colaunch/internal/plugins"
)

type alerted struct{ event, title, body string }

func alertsOf(m *Manager) *[]alerted {
	var got []alerted
	m.Alert = func(event, title, body string) { got = append(got, alerted{event, title, body}) }
	return &got
}

// Channels' tokens and keys stay out of settings.json, come back from
// Secrets, and move there from files written with them in the open.
func TestChannelSecretsStayOutOfFiles(t *testing.T) {
	dir, sec := t.TempDir(), mapSecrets{}
	m := New(dir, sec)
	ch := m.SaveChannel(notify.Channel{Kind: notify.KindTelegram, Name: "phone", Enabled: true, Events: []string{"ready"},
		Params: map[string]string{"chat_id": "42"}, Secret: map[string]string{"bot_token": "123:SECRET-token"}})
	m.Close()
	if fileHas(t, filepath.Join(dir, "settings.json"), "SECRET-token") {
		t.Error("settings.json has the bot token")
	}
	if !fileHas(t, filepath.Join(dir, "settings.json"), `"chat_id": "42"`) {
		t.Error("settings.json lost the chat id")
	}
	m = New(dir, sec)
	got, ok := m.Channel(ch.ID)
	if !ok || got.Secret["bot_token"] != "123:SECRET-token" || got.Params["chat_id"] != "42" {
		t.Errorf("came back as %+v", got)
	}
	m.DeleteChannel(ch.ID)
	m.Close()
	if _, ok := sec[channelSecretKey(ch.ID)]; ok {
		t.Error("a deleted channel's secrets stayed")
	}

	dir, sec = t.TempDir(), mapSecrets{}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"notifications": {"channels": [
		{"id": "c1", "kind": "bark", "enabled": true, "params": {"device_key": "OLDKEY1"}},
		{"id": "c2", "kind": "discord", "secret": {"url": "https://discord.com/api/webhooks/1/OLDHOOK"}}]}}`), 0o600)
	m = New(dir, sec)
	defer m.Close()
	for _, s := range []string{"OLDKEY1", "OLDHOOK"} {
		if fileHas(t, filepath.Join(dir, "settings.json"), s) {
			t.Errorf("settings.json still has %s", s)
		}
	}
	if c, _ := m.Channel("c1"); c.Value("device_key") != "OLDKEY1" {
		t.Errorf("c1 came back as %+v", c)
	}
	if c, _ := m.Channel("c2"); !strings.HasSuffix(c.Value("url"), "OLDHOOK") {
		t.Errorf("c2 came back as %+v", c)
	}
}

// Failures, giving up grabbing, and stops are noticed with the model's
// name as parsed, and nothing that gives access.
func TestSessionAlerts(t *testing.T) {
	m := New(t.TempDir(), mapSecrets{"hf-token": []byte("hf_abcdefghijkl")})
	defer m.Close()
	got := alertsOf(m)
	s := &Session{Phase: PhaseDeploying, sessionRecord: sessionRecord{ID: "s1", Plugin: "llama",
		Values:  map[string]string{"repo": "https://huggingface.co/unsloth/Qwen3-8B-GGUF?download=true&token=abc", "api_key": "sk-local-123456"},
		Created: time.Now().Add(-90 * time.Minute)}}
	s.init()
	m.Sessions = []*Session{s}
	m.fail(s, &Account{}, errors.New("hf: GET https://huggingface.co/api/x?token=hf_abcdefghijkl failed for hf_abcdefghijkl with sk-local-123456"))
	if len(*got) != 1 {
		t.Fatalf("alerts %v", *got)
	}
	a := (*got)[0]
	if a.event != notify.EventFailed || !strings.Contains(a.title, "unsloth/Qwen3-8B-GGUF") || strings.Contains(a.title, "huggingface.co") {
		t.Errorf("failed as %+v", a)
	}
	for _, leak := range []string{"hf_abcdefghijkl", "sk-local-123456", "token=", "download="} {
		if strings.Contains(a.title+a.body, leak) {
			t.Errorf("the notice has %q: %+v", leak, a)
		}
	}

	*got = nil
	s.Phase = PhaseWaiting
	m.fail(s, &Account{}, fmt.Errorf("%w after 40 tries in 2h0m0s: %w", errGaveUp, colab.ErrUnavailable))
	if len(*got) != 1 || (*got)[0].event != notify.EventGrabGaveUp {
		t.Errorf("gave up as %v", *got)
	}

	// The runtime gone, its watchdog having stopped it when idle.
	*got = nil
	s.Phase = PhaseRunning
	s.Result = &plugins.Result{Model: "Qwen3-8B"}
	s.Status = &agent.Status{}
	s.Status.Stopped = &struct {
		Reason string  `json:"reason"`
		Time   float64 `json:"time"`
	}{Reason: "idle"}
	m.gone(s)
	if len(*got) != 1 || (*got)[0].event != notify.EventStopped || !strings.Contains((*got)[0].body, "stopped when idle") || !strings.Contains((*got)[0].title, "Qwen3-8B") {
		t.Errorf("stopped as %v", *got)
	}
}

// An account's balance going under the threshold is noticed once each
// time it does.
func TestLowBalanceOnce(t *testing.T) {
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	got := alertsOf(m)
	m.Settings.Notifications.BalanceBelow = 10
	a := acct("ada@example.com")
	for _, b := range []float64{50, 9, 8, 7, 12, 9.5, 0, 0} {
		a.Quota = &colab.Quota{Balance: b}
		m.checkLowBalance(a)
	}
	if len(*got) != 2 {
		t.Fatalf("alerts %v", *got)
	}
	if (*got)[0].event != notify.EventBalanceLow || !strings.Contains((*got)[0].body, "9.0") {
		t.Errorf("got %+v", (*got)[0])
	}
	// An account that never had compute units isn't low.
	*got = nil
	b := acct("free@example.com")
	b.Quota = &colab.Quota{Balance: 0}
	m.checkLowBalance(b)
	if len(*got) != 0 {
		t.Errorf("a free account: %v", *got)
	}
}

// Channels get the events they subscribed to; one that sends from the
// runtime doesn't get from the app the stop the runtime told it of.
func TestAlertsReachChannels(t *testing.T) {
	type hit struct{ path, event string }
	hits := make(chan hit, 10)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var v map[string]string
		json.Unmarshal(b, &v)
		hits <- hit{r.URL.Path, v["event"]}
	}))
	defer ts.Close()
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	hook := func(name string, runtime bool, events ...string) notify.Channel {
		return notify.Channel{ID: name, Kind: notify.KindWebhook, Name: name, Enabled: true, Events: events, Runtime: runtime,
			Secret: map[string]string{"url": ts.URL + "/" + name}}
	}
	m.Settings.Notifications.Channels = []notify.Channel{
		hook("app", false, "stopped", "ready"),
		hook("rt", true, "stopped"),
		hook("ready-only", false, "ready"),
		{ID: "off", Kind: notify.KindWebhook, Events: []string{"stopped"}, Secret: map[string]string{"url": ts.URL + "/off"}},
	}
	m.alert(notify.EventStopped, "t", "b", true)
	m.alert(notify.EventStopped, "t", "b", false) // within 30 s of the first: held back for "app"
	var paths []string
	timeout := time.After(10 * time.Second)
	for len(paths) < 2 {
		select {
		case h := <-hits:
			paths = append(paths, h.path)
		case <-timeout:
			t.Fatalf("got only %v", paths)
		}
	}
	select {
	case h := <-hits:
		paths = append(paths, h.path)
	case <-time.After(300 * time.Millisecond):
	}
	if strings.Join(paths, " ") != "/app /rt" && strings.Join(paths, " ") != "/rt /app" {
		t.Errorf("notices went to %v", paths)
	}

	// The runtime's requests: its channels only, the reason left for it.
	m.Settings.Notifications.Channels[1].Secret["url"] = ts.URL + "/rt?key=SECRET"
	s := &Session{sessionRecord: sessionRecord{Plugin: "llama", Values: map[string]string{"repo": "a/b-GGUF"}}}
	n := m.runtimeNotices(s)
	if len(n.Requests) != 1 || n.Requests[0].URL != ts.URL+"/rt?key=SECRET" || !strings.Contains(n.Requests[0].Body, notify.ReasonPlaceholder) || !strings.Contains(n.Requests[0].Body, "a/b-GGUF") {
		t.Errorf("runtime notices %+v", n)
	}
	if n.Reasons["idle"] != "stopped when idle" || n.Reasons["budget"] == "" || n.Reasons["schedule"] == "" {
		t.Errorf("reasons %v", n.Reasons)
	}
}
