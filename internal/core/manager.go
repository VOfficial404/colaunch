package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/google"
	"colaunch/internal/hf"
	"colaunch/internal/plugins"
)

// DefaultOAuthClient is the OAuth client builds come with, set at build
// time with -ldflags "-X colaunch/internal/core.defaultClientID=...". The
// repository keeps none.
var (
	defaultClientID     string
	defaultClientSecret string
)

// DefaultClient is the client builds come with, if any.
func DefaultClient() google.Client {
	return google.Client{ID: defaultClientID, Secret: defaultClientSecret}
}

// Manager is the app's state.
type Manager struct {
	Dir     string
	Secrets Secrets
	// Post runs fn on the main thread, and draws the window again.
	Post func(fn func())
	// Notify tells the user something, as a toast: kind is "info",
	// "success" or "error".
	Notify func(kind, title, text string)
	// Lang is the language plugins write their messages in.
	Lang func() string

	Settings Settings
	Accounts []*Account
	Sessions []*Session
	// PluginErrors are why plugins in the plugins folder didn't load.
	PluginErrors []error

	ctx    context.Context
	cancel context.CancelFunc
	http   *http.Client
}

// New loads the manager's state from dir. Call Start once Post works.
func New(dir string, secrets Secrets) *Manager {
	m := &Manager{
		Dir:      dir,
		Secrets:  secrets,
		Post:     func(fn func()) { fn() },
		Notify:   func(string, string, string) {},
		Lang:     func() string { return "en" },
		Settings: defaultSettings(),
		http:     &http.Client{Timeout: 60 * time.Second},
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	if err := readJSON(filepath.Join(dir, "settings.json"), &m.Settings); err != nil {
		log.Printf("settings: %v", err)
	}
	if m.Settings.Deploy.Values == nil {
		m.Settings.Deploy.Values = map[string]map[string]string{}
	}
	if m.Settings.MaxAttempts <= 0 {
		m.Settings.MaxAttempts = 6
	}
	m.applySettings()
	var st stateFile
	if err := readJSON(filepath.Join(dir, "state.json"), &st); err != nil {
		log.Printf("state: %v", err)
	}
	for _, r := range st.Accounts {
		m.Accounts = append(m.Accounts, m.newAccount(r, nil))
	}
	for _, r := range st.Sessions {
		s := &Session{sessionRecord: r, Phase: PhaseReattaching}
		s.init()
		m.Sessions = append(m.Sessions, s)
	}
	m.PluginErrors = plugins.LoadDir(m.PluginsDir())
	return m
}

// PluginsDir is where users put their plugins.
func (m *Manager) PluginsDir() string { return filepath.Join(m.Dir, "plugins") }

// ReloadPlugins loads the plugins folder again.
func (m *Manager) ReloadPlugins() { m.PluginErrors = plugins.LoadDir(m.PluginsDir()) }

// Start finds the runtimes of the sessions kept, and starts watching
// balances and runtimes.
func (m *Manager) Start() {
	for _, s := range m.Sessions {
		go m.reattach(s)
	}
	for _, a := range m.Accounts {
		go m.refreshAccount(a)
	}
	go m.loop()
}

// RefreshConnections checks live sessions when the app returns to the foreground.
// Call it from the main thread.
func (m *Manager) RefreshConnections() {
	for _, s := range m.Sessions {
		if s.Phase == PhaseRunning {
			go m.watch(s)
		}
	}
}

// Close stops the manager's work, leaving the runtimes running: their
// watchdogs keep their limits.
func (m *Manager) Close() {
	m.cancel()
	for _, s := range m.Sessions {
		s.closeConns()
	}
}

// Client returns the OAuth client to sign in with: the settings', else the
// one the build came with.
func (m *Manager) Client() google.Client {
	if m.Settings.OAuth.Valid() {
		return m.Settings.OAuth
	}
	return DefaultClient()
}

// SaveSettings keeps the settings.
func (m *Manager) SaveSettings() {
	m.applySettings()
	if err := writeJSON(filepath.Join(m.Dir, "settings.json"), m.Settings); err != nil {
		m.Notify("error", "settings", err.Error())
	}
}

func (m *Manager) applySettings() {
	hf.Endpoint = "https://huggingface.co"
	if m.Settings.HFEndpoint != "" {
		hf.Endpoint = m.Settings.HFEndpoint
	}
}

// HFToken is the Hugging Face token, for gated models.
func (m *Manager) HFToken() string {
	b, _ := m.Secrets.Get("hf-token")
	return string(b)
}

// SetHFToken keeps the Hugging Face token.
func (m *Manager) SetHFToken(t string) {
	if t == "" {
		m.Secrets.Delete("hf-token")
		return
	}
	m.Secrets.Set("hf-token", []byte(t))
}

func (m *Manager) saveState() {
	var st stateFile
	for _, a := range m.Accounts {
		st.Accounts = append(st.Accounts, a.accountRecord)
	}
	for _, s := range m.Sessions {
		if !s.Ended() {
			st.Sessions = append(st.Sessions, s.sessionRecord)
		}
	}
	if err := writeJSON(filepath.Join(m.Dir, "state.json"), st); err != nil {
		log.Printf("state: %v", err)
	}
}

// loop watches balances and runtimes.
func (m *Manager) loop() {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
		}
		n++
		var accounts []*Account
		var sessions []*Session
		done := make(chan struct{})
		m.Post(func() {
			accounts = append(accounts, m.Accounts...)
			sessions = append(sessions, m.Sessions...)
			close(done)
		})
		select {
		case <-done:
		case <-m.ctx.Done():
			return
		}
		if n%4 == 0 {
			for _, a := range accounts {
				go m.refreshAccount(a)
			}
		}
		for _, s := range sessions {
			if s.Phase == PhaseRunning && n%2 == 0 {
				go m.watch(s)
			}
		}
	}
}

// Account returns an account by its email.
func (m *Manager) Account(email string) *Account {
	for _, a := range m.Accounts {
		if a.Email == email {
			return a
		}
	}
	return nil
}

// Active returns the account new runtimes go to.
func (m *Manager) Active() *Account {
	if a := m.Account(m.Settings.ActiveAccount); a != nil {
		return a
	}
	if len(m.Accounts) > 0 {
		return m.Accounts[0]
	}
	return nil
}

// SetActive makes an account the one new runtimes go to.
func (m *Manager) SetActive(email string) {
	m.Settings.ActiveAccount = email
	m.SaveSettings()
}

// Live returns the sessions that haven't ended, newest first.
func (m *Manager) Live() []*Session {
	var out []*Session
	for _, s := range m.Sessions {
		if !s.Ended() {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Session returns a session by its ID.
func (m *Manager) Session(id string) *Session {
	for _, s := range m.Sessions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Forget removes an ended session.
func (m *Manager) Forget(s *Session) {
	for i, x := range m.Sessions {
		if x == s {
			m.Sessions = append(m.Sessions[:i], m.Sessions[i+1:]...)
			break
		}
	}
	m.saveState()
}

// Unmanaged returns the runtimes of the accounts that no session has, as
// those assigned in Colab's own pages or left by a crash.
func (m *Manager) Unmanaged() []UnmanagedRuntime {
	have := map[string]bool{}
	for _, s := range m.Sessions {
		if !s.Ended() {
			have[s.RuntimeID] = true
		}
	}
	var out []UnmanagedRuntime
	for _, a := range m.Accounts {
		for _, r := range a.Runtimes {
			if !have[r.ID()] {
				out = append(out, UnmanagedRuntime{Account: a, Runtime: r})
			}
		}
	}
	return out
}

// UnmanagedRuntime is a runtime of an account that no session has.
type UnmanagedRuntime struct {
	Account *Account
	Runtime colab.Runtime
}

// Release gives an unmanaged runtime back.
func (m *Manager) Release(u UnmanagedRuntime) {
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		err := u.Account.Client.DeleteRuntime(ctx, u.Runtime.ID())
		m.Post(func() {
			if err != nil {
				m.Notify("error", u.Runtime.Spec.String(), err.Error())
				return
			}
			m.Notify("success", u.Runtime.Spec.String(), "")
		})
		m.refreshAccount(u.Account)
	}()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	var api *colab.APIError
	if errors.As(err, &api) && api.Message != "" {
		return api.Message
	}
	return err.Error()
}

func fmtGB(mb int64) string { return fmt.Sprintf("%.0f GB", float64(mb)/1024) }
