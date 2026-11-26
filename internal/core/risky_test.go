package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/plugins"
)

// webPlugin is a task served through a web page, which stops at Validate.
type webPlugin struct{ kind plugins.PortKind }

func (webPlugin) ID() string                { return "webtest" }
func (webPlugin) Name() plugins.Text        { return plugins.Text{"en": "Web test"} }
func (webPlugin) Description() plugins.Text { return plugins.Text{"en": ""} }
func (webPlugin) Fields() []plugins.Field   { return nil }
func (p webPlugin) Ports() []plugins.Port {
	return []plugins.Port{{Name: "ui", Remote: 7860, Local: 7860, Kind: p.kind}}
}
func (webPlugin) Validate(context.Context, map[string]string, string) (*plugins.Hardware, error) {
	return nil, errors.New("test: stop here")
}
func (webPlugin) Deploy(context.Context, *plugins.Env) (*plugins.Result, error) { return nil, nil }
func (webPlugin) Stop(context.Context, *colab.Shell) error                      { return nil }

func register(t *testing.T, p plugins.Plugin) {
	if err := plugins.Register(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plugins.Unregister(p.ID()) })
}

// managerWith is a manager whose account has quota q, with what goroutines
// post dropped.
func managerWith(t *testing.T, q *colab.Quota) *Manager {
	m := New(t.TempDir(), mapSecrets{})
	t.Cleanup(m.Close)
	m.Post = func(func()) {}
	a := &Account{accountRecord: accountRecord{Email: "a@example.com"}, Quota: q}
	m.Accounts = []*Account{a}
	m.Settings.ActiveAccount = a.Email
	return m
}

// Grabbing waits 10 seconds between rounds at the least, 3 with fast
// grabbing on.
func TestFastGrabFloor(t *testing.T) {
	for _, fast := range []bool{false, true} {
		m := New(t.TempDir(), mapSecrets{})
		m.Settings.Risky.FastGrab = fast
		machine := m.grabMachine(Machine{Variant: "gpu", Accelerator: "T4", Grab: true, RetrySeconds: 3})
		var waits []time.Duration
		old := grabSleep
		grabSleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
		n := 0
		_, err := m.grab(context.Background(), &Session{}, machine, 0, func(colab.Spec) error {
			if n++; n < 2 {
				return colab.ErrUnavailable
			}
			return nil
		})
		grabSleep = old
		want := 10 * time.Second
		if fast {
			want = 3 * time.Second
		}
		if err != nil || len(waits) != 1 || waits[0] != want {
			t.Errorf("fast %v: waits %v, %v; want %v", fast, waits, err, want)
		}
	}
	if (Settings{}).MinRetry() != 10 {
		t.Error("settings without Risky don't keep 10 seconds")
	}
}

// On the free tier, tasks used through a web page don't launch unless the
// setting allows them; the llama plugin's API isn't one.
func TestFreeWebUIRefused(t *testing.T) {
	register(t, webPlugin{kind: plugins.PortWeb})
	free := &colab.Quota{Tier: "PRO", Balance: 0}
	m := managerWith(t, free)
	form := DeployForm{Plugin: "webtest", Values: map[string]map[string]string{}}
	if _, err := m.Launch(form); !errors.Is(err, ErrFreeWebUI) || len(m.Sessions) != 0 {
		t.Fatalf("launched a web task on the free tier: %v, %d sessions", err, len(m.Sessions))
	}
	m.Settings.Risky.FreeWebUI = true
	if _, err := m.Launch(form); err != nil || len(m.Sessions) != 1 {
		t.Errorf("with the setting on: %v, %d sessions", err, len(m.Sessions))
	}
	// Paid units: no check.
	paid := managerWith(t, &colab.Quota{Tier: "PRO", Balance: 50})
	if _, err := paid.Launch(form); err != nil {
		t.Errorf("paid account refused: %v", err)
	}
	if !WebTask(plugins.Get("comfyui")) || WebTask(plugins.Get("llama")) || WebTask(webPlugin{kind: plugins.PortOpenAI}) {
		t.Error("WebTask took the wrong plugins")
	}
	for tier, want := range map[string]bool{"NONE": true, "FREE": true, "UNSPECIFIED": true, "PRO": false} {
		if got := (&Account{Quota: &colab.Quota{Tier: tier, Balance: 10}}).FreeTier(); got != want {
			t.Errorf("tier %s: free %v", tier, got)
		}
	}
}

// Public links are refused with the setting off, and for an OpenAI API
// deployed without an API key.
func TestPublicLinkRefused(t *testing.T) {
	m := managerWith(t, &colab.Quota{Tier: "PRO", Balance: 50})
	opened := 0
	old := openShare
	openShare = func(context.Context, *colab.Shell, int) (string, error) {
		opened++
		return "https://x.trycloudflare.com", nil
	}
	t.Cleanup(func() { openShare = old })
	s := &Session{Phase: PhaseRunning}
	s.Account, s.Plugin = "a@example.com", "llama"
	s.Result = &plugins.Result{Model: "m"}
	s.Forwards = []*ForwardState{{Port: plugins.Port{Name: "llama-server", Remote: plugins.LlamaPort, Kind: plugins.PortOpenAI}}}
	m.Sessions = []*Session{s}
	if err := m.StartPublicLink(s, plugins.LlamaPort); !errors.Is(err, ErrPublicLinksOff) {
		t.Errorf("setting off: %v", err)
	}
	m.Settings.Risky.PublicLinks = true
	if err := m.StartPublicLink(s, plugins.LlamaPort); !errors.Is(err, ErrPublicLinkNeedsKey) {
		t.Errorf("no API key: %v", err)
	}
	if s.HasPublicLink() || opened != 0 {
		t.Errorf("a refused link started: %v, %d", s.Public, opened)
	}
	s.Forwards = append(s.Forwards, &ForwardState{Port: plugins.Port{Name: "ssh", Remote: 22, Kind: plugins.PortTCP}})
	if err := m.StartPublicLink(s, 22); err == nil {
		t.Error("made a TCP port public")
	}
	s.Result.APIKey = "sk-1"
	if err := m.StartPublicLink(s, plugins.LlamaPort); err != nil || !s.PublicLink(plugins.LlamaPort).Live() {
		t.Errorf("with a key: %v", err)
	}
	m.StopPublicLink(s, plugins.LlamaPort)
	if s.HasPublicLink() {
		t.Error("the link stayed after closing it")
	}
}

// Stopping a session forgets its public links.
func TestStopDropsPublicLinks(t *testing.T) {
	m := managerWith(t, &colab.Quota{Tier: "PRO", Balance: 50})
	s := &Session{Phase: PhaseRunning}
	s.Public = map[int]*PublicLink{8188: {Remote: 8188, URL: "https://x.trycloudflare.com"}}
	m.Sessions = []*Session{s}
	m.Stop(s, "user")
	if s.HasPublicLink() {
		t.Error("public links survived stopping")
	}
}
