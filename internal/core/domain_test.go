package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/plugins"
)

// domainHooks replaces what reaches Cloudflare and the runtime, and records
// what was asked of them.
type domainHooks struct {
	published []int
	opened    []string
	stopped   []string
}

func hookDomains(t *testing.T) *domainHooks {
	h := &domainHooks{}
	oldPub, oldOpen, oldStop, oldShell := publishDomain, openDomain, stopDomain, domainShell
	publishDomain = func(_ context.Context, d DomainConfig, port int) (string, string, error) {
		h.published = append(h.published, port)
		return "api-tunnel-token", d.Host(), nil
	}
	openDomain = func(_ context.Context, _ *colab.Shell, config, token string) error {
		h.opened = append(h.opened, config+":"+token)
		return nil
	}
	stopDomain = func(_ context.Context, _ *colab.Shell, config string) error {
		h.stopped = append(h.stopped, config)
		return nil
	}
	domainShell = func(*Manager, context.Context, *Session, *Account) (*colab.Shell, error) { return nil, nil }
	t.Cleanup(func() { publishDomain, openDomain, stopDomain, domainShell = oldPub, oldOpen, oldStop, oldShell })
	return h
}

// runningSession is a running session of plugin with port, in m.
func runningSession(m *Manager, id string, port plugins.Port) *Session {
	s := &Session{Phase: PhaseRunning}
	s.ID, s.Account, s.Plugin = id, "a@example.com", "llama"
	s.Result = &plugins.Result{Model: "m"}
	s.Forwards = []*ForwardState{{Port: port}}
	m.Sessions = append(m.Sessions, s)
	return s
}

var llamaPort = plugins.Port{Name: "llama-server", Remote: plugins.LlamaPort, Kind: plugins.PortOpenAI}

// postChan has what goroutines post run on the test's goroutine, by
// runUntil.
func postChan(m *Manager) chan func() {
	ch := make(chan func(), 64)
	m.Post = func(fn func()) { ch <- fn }
	return ch
}

// runUntil runs what was posted until done reports true.
func runUntil(t *testing.T, posted chan func(), done func() bool) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for !done() {
		select {
		case fn := <-posted:
			fn()
		case <-timeout:
			t.Fatal("timed out")
		}
	}
}

// Domains are refused with public links off, for an API without a key,
// and for a configuration that lacks its token.
func TestDomainLinkRefused(t *testing.T) {
	h := hookDomains(t)
	m := managerWith(t, &colab.Quota{Tier: "PRO", Balance: 50})
	d := m.SaveDomain(DomainConfig{Name: "Mine", Mode: DomainToken, Hostname: "ai.example.com", TunnelToken: "tun-tok"})
	s := runningSession(m, "s1", llamaPort)
	if err := m.StartDomainLink(s, plugins.LlamaPort, d.ID); !errors.Is(err, ErrPublicLinksOff) {
		t.Errorf("public links off: %v", err)
	}
	m.Settings.Risky.PublicLinks = true
	if err := m.StartDomainLink(s, plugins.LlamaPort, d.ID); !errors.Is(err, ErrPublicLinkNeedsKey) {
		t.Errorf("no API key: %v", err)
	}
	s.Result.APIKey = "sk-1"
	empty := m.SaveDomain(DomainConfig{Name: "Empty", Mode: DomainToken, Hostname: "x.example.com"})
	if err := m.StartDomainLink(s, plugins.LlamaPort, empty.ID); err == nil {
		t.Error("published with no tunnel token")
	}
	if err := m.StartDomainLink(s, plugins.LlamaPort, "nope"); !errors.Is(err, ErrNoDomain) {
		t.Errorf("no such config: %v", err)
	}
	if s.HasDomainLink() || len(h.opened) != 0 {
		t.Errorf("a refused domain started: %v %v", s.Domains, h.opened)
	}
}

// A configuration serves one port at a time: binding it elsewhere is
// refused until it's disconnected; binding the same port again is nothing.
func TestDomainBoundOnce(t *testing.T) {
	h := hookDomains(t)
	m := managerWith(t, &colab.Quota{Tier: "PRO", Balance: 50})
	posted := postChan(m)
	m.Settings.Risky.PublicLinks = true
	d := m.SaveDomain(DomainConfig{Name: "Mine", Mode: DomainToken, Hostname: "https://AI.example.com/", TunnelToken: "tun-tok"})
	web := plugins.Port{Name: "ui", Remote: 7860, Kind: plugins.PortWeb}
	s1 := runningSession(m, "s1", web)
	s2 := runningSession(m, "s2", plugins.Port{Name: "comfy", Remote: 8188, Kind: plugins.PortComfy})

	if err := m.StartDomainLink(s1, 7860, d.ID); err != nil {
		t.Fatal(err)
	}
	runUntil(t, posted, func() bool { return !s1.DomainLink(7860).Starting })
	l := s1.DomainLink(7860)
	if l == nil || l.Starting || l.URL != "https://ai.example.com" || l.Err != "" || l.Protected {
		t.Fatalf("link %+v", l)
	}
	if len(h.opened) != 1 || h.opened[0] != d.ID+":tun-tok" || len(h.published) != 0 {
		t.Errorf("opened %v, published %v", h.opened, h.published)
	}
	if err := m.StartDomainLink(s1, 7860, d.ID); err != nil || len(h.opened) != 1 {
		t.Errorf("binding the same port again: %v, %v", err, h.opened)
	}
	if err := m.StartDomainLink(s2, 8188, d.ID); !errors.Is(err, ErrDomainBound) || s2.HasDomainLink() {
		t.Errorf("binding elsewhere: %v", err)
	}
	if bs, bl := m.DomainBinding(d.ID); bs != s1 || bl != l {
		t.Errorf("binding %v %v", bs, bl)
	}
	// What the confirmation does: disconnect the old, then bind.
	m.StopDomainLink(s1, 7860)
	if s1.HasDomainLink() {
		t.Error("the link stayed after disconnecting it")
	}
	if err := m.StartDomainLink(s2, 8188, d.ID); err != nil {
		t.Fatal(err)
	}
	runUntil(t, posted, func() bool { return !s2.DomainLink(8188).Starting })
	if s2.DomainLink(8188).URL != "https://ai.example.com" {
		t.Errorf("rebound: %+v", s2.DomainLink(8188))
	}
	// Deleting the configuration disconnects it.
	m.DeleteDomain(d.ID)
	if s2.HasDomainLink() {
		t.Error("deleting the configuration left its link")
	}
	if _, ok := m.Domain(d.ID); ok {
		t.Error("the configuration stayed")
	}
}

// With an API token, publishing goes through Cloudflare's API to the port,
// then runs the tunnel with the token it gave.
func TestDomainAPIPublishes(t *testing.T) {
	h := hookDomains(t)
	m := managerWith(t, &colab.Quota{Tier: "PRO", Balance: 50})
	posted := postChan(m)
	m.Settings.Risky.PublicLinks = true
	d := m.SaveDomain(DomainConfig{Name: "API", Mode: DomainAPI, APIToken: "cf-api", AccountID: "acc", ZoneID: "z", ZoneName: "example.com",
		Subdomain: "GPU", Access: true, AccessEmails: []string{"ada@example.com"}})
	s := runningSession(m, "s1", plugins.Port{Name: "comfy", Remote: 8188, Kind: plugins.PortComfy})
	if err := m.StartDomainLink(s, 8188, d.ID); err != nil {
		t.Fatal(err)
	}
	runUntil(t, posted, func() bool { return !s.DomainLink(8188).Starting })
	l := s.DomainLink(8188)
	if l.URL != "https://gpu.example.com" || !l.Protected {
		t.Errorf("link %+v", l)
	}
	if len(h.published) != 1 || h.published[0] != 8188 || len(h.opened) != 1 || h.opened[0] != d.ID+":api-tunnel-token" {
		t.Errorf("published %v, opened %v", h.published, h.opened)
	}
	// Removing what it made waits until it's disconnected.
	var got error
	m.DeleteDomainResources(d.ID, func(err error) { got = err })
	if !errors.Is(got, ErrDomainBound) {
		t.Errorf("removing while bound: %v", got)
	}
	removed := 0
	old := removeDomain
	removeDomain = func(context.Context, DomainConfig) error { removed++; return nil }
	t.Cleanup(func() { removeDomain = old })
	m.StopDomainLink(s, 8188)
	got = errors.New("not called")
	called := false
	m.DeleteDomainResources(d.ID, func(err error) { got, called = err, true })
	runUntil(t, posted, func() bool { return called })
	if got != nil || removed != 1 {
		t.Errorf("removing: %v, %d", got, removed)
	}
}

// Stopping a session, or turning public links off, disconnects its
// domains.
func TestStopDropsDomainLinks(t *testing.T) {
	hookDomains(t)
	m := managerWith(t, &colab.Quota{Tier: "PRO", Balance: 50})
	s := &Session{Phase: PhaseRunning}
	s.Domains = map[int]*DomainLink{8188: {Config: "c1", Remote: 8188, URL: "https://ai.example.com"}}
	m.Sessions = []*Session{s}
	if !s.Exposed() {
		t.Fatal("not exposed")
	}
	m.Stop(s, "user")
	if s.HasDomainLink() || s.Exposed() {
		t.Error("domains survived stopping")
	}
	if bs, _ := m.DomainBinding("c1"); bs != nil {
		t.Error("the configuration is still bound")
	}

	s2 := &Session{Phase: PhaseRunning}
	s2.Account = "a@example.com"
	s2.Domains = map[int]*DomainLink{7860: {Config: "c2", Remote: 7860, URL: "https://b.example.com"}}
	m.Sessions = append(m.Sessions, s2)
	m.StopAllPublicLinks()
	if s2.HasDomainLink() {
		t.Error("turning public links off left a domain")
	}
}

// Domain tokens stay out of settings.json, come back from Secrets, and
// move there from files written with them in the open.
func TestDomainSecretsStayOutOfFiles(t *testing.T) {
	dir, sec := t.TempDir(), mapSecrets{}
	m := New(dir, sec)
	d := m.SaveDomain(DomainConfig{Name: "Mine", Mode: DomainToken, Hostname: "ai.example.com", TunnelToken: "secret-tunnel"})
	a := m.SaveDomain(DomainConfig{Name: "API", Mode: DomainAPI, APIToken: "secret-api", AccountID: "acc", ZoneID: "z", ZoneName: "example.com", Subdomain: "gpu"})
	m.Close()
	path := filepath.Join(dir, "settings.json")
	for _, s := range []string{"secret-tunnel", "secret-api"} {
		if fileHas(t, path, s) {
			t.Errorf("settings.json has %s", s)
		}
	}
	if !fileHas(t, path, "ai.example.com") || !fileHas(t, path, `"zone_id": "z"`) {
		t.Error("settings.json lost the configurations")
	}
	m = New(dir, sec)
	if got, _ := m.Domain(d.ID); got.TunnelToken != "secret-tunnel" {
		t.Errorf("tunnel token came back as %q", got.TunnelToken)
	}
	if got, _ := m.Domain(a.ID); got.APIToken != "secret-api" || got.Host() != "gpu.example.com" {
		t.Errorf("API config came back as %+v", got)
	}
	m.DeleteDomain(d.ID)
	if _, ok := sec[domainSecretKey(d.ID)]; ok {
		t.Error("a deleted configuration's token stayed")
	}
	m.Close()

	old, sec2 := t.TempDir(), mapSecrets{}
	os.WriteFile(filepath.Join(old, "settings.json"), []byte(`{"risky": {"public_links": true, "domains": [{"id": "c9", "name": "Old", "mode": "token", "hostname": "x.example.com", "tunnel_token": "old-tunnel-token"}]}}`), 0o600)
	m = New(old, sec2)
	defer m.Close()
	if fileHas(t, filepath.Join(old, "settings.json"), "old-tunnel-token") {
		t.Error("settings.json still has the token")
	}
	if got, _ := m.Domain("c9"); got.TunnelToken != "old-tunnel-token" || len(sec2[domainSecretKey("c9")]) == 0 {
		t.Errorf("the token was lost moving it: %+v %v", got, sec2)
	}
}
