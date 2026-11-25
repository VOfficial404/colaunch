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
	"colaunch/internal/notify"
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
	// Peek runs fn on the main thread without drawing the window again,
	// for reading the state; nil does as Post.
	Peek func(fn func())
	// Notify tells the user something, as a toast: kind is "info",
	// "success" or "error".
	Notify func(kind, title, text string)
	// Lang is the language plugins write their messages in.
	Lang func() string
	// Alert, when set, hears each notice as it goes to the channels,
	// without secrets; any channel's sending is done apart.
	Alert func(event, title, body string)

	Settings Settings
	Accounts []*Account
	Sessions []*Session
	// PluginErrors are why plugins in the plugins folder didn't load.
	PluginErrors []error

	ctx    context.Context
	cancel context.CancelFunc
	http   *http.Client
	work   workState
	// notifier sends notices to the channels.
	notifier *notify.Dispatcher
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
		notifier: notify.NewDispatcher(),
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	// The plugins first: their fields say which values are kept in Secrets.
	m.PluginErrors = plugins.LoadDir(m.PluginsDir())
	if err := readJSON(filepath.Join(dir, "settings.json"), &m.Settings); err != nil {
		log.Printf("settings: %v", err)
	}
	if m.Settings.Deploy.Values == nil {
		m.Settings.Deploy.Values = map[string]map[string]string{}
	}
	plainSettings := m.loadSettingSecrets()
	if m.Settings.MaxAttempts <= 0 {
		m.Settings.MaxAttempts = 6
	}
	if err := m.applySettings(); err != nil {
		log.Printf("settings: %v", err)
	}
	var st stateFile
	if err := readJSON(filepath.Join(dir, "state.json"), &st); err != nil {
		log.Printf("state: %v", err)
	}
	for _, r := range st.Accounts {
		m.Accounts = append(m.Accounts, m.newAccount(r, nil))
	}
	plainState := false
	for _, r := range st.Sessions {
		if m.loadSessionSecrets(&r) {
			plainState = true
		}
		s := &Session{sessionRecord: r, Phase: PhaseReattaching}
		s.init()
		m.Sessions = append(m.Sessions, s)
	}
	// Files written before secrets were kept apart have them in the open:
	// write them again without.
	if plainSettings {
		m.SaveSettings()
	}
	if plainState {
		m.saveState()
	}
	return m
}

// PluginsDir is where users put their plugins.
func (m *Manager) PluginsDir() string { return filepath.Join(m.Dir, "plugins") }

// ReloadPlugins loads the plugins folder again.
func (m *Manager) ReloadPlugins() { m.PluginErrors = plugins.LoadDir(m.PluginsDir()) }

// Start finds the runtimes of the sessions kept, and starts watching
// balances and runtimes.
// Call it from the main thread.
func (m *Manager) Start() {
	for _, s := range m.Sessions {
		go m.reattach(s, m.Account(s.Account))
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
		if a := m.Account(s.Account); a != nil && s.Phase == PhaseRunning {
			go m.watch(s, a)
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

// SaveSettings keeps the settings, the values of password fields in
// Secrets.
func (m *Manager) SaveSettings() {
	if err := m.applySettings(); err != nil {
		m.Notify("error", "settings", err.Error())
	}
	if err := writeJSON(filepath.Join(m.Dir, "settings.json"), m.settingsToSave()); err != nil {
		m.Notify("error", "settings", err.Error())
	}
}

// applySettings points the Hugging Face requests at the endpoint of the
// settings. One that isn't https is refused: requests fail with the
// reason rather than go to it, or quietly elsewhere.
func (m *Manager) applySettings() error {
	if m.Settings.HFEndpoint == "" {
		hf.Endpoint = hf.Official
		return nil
	}
	e, err := hf.CheckEndpoint(m.Settings.HFEndpoint)
	if err != nil {
		hf.Endpoint = m.Settings.HFEndpoint
		return err
	}
	hf.Endpoint = e
	return nil
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

// saveState keeps the accounts and the live sessions, the sessions'
// secrets in Secrets, and forgets those of the sessions it no longer
// keeps.
func (m *Manager) saveState() {
	path := filepath.Join(m.Dir, "state.json")
	var old stateFile
	readJSON(path, &old)
	var st stateFile
	for _, a := range m.Accounts {
		st.Accounts = append(st.Accounts, a.accountRecord)
	}
	kept := map[string]bool{}
	for _, s := range m.Sessions {
		if !s.Ended() {
			st.Sessions = append(st.Sessions, m.recordToSave(s.sessionRecord))
			kept[s.ID] = true
		}
	}
	if err := writeJSON(path, st); err != nil {
		log.Printf("state: %v", err)
		return
	}
	for _, r := range old.Sessions {
		if r.ID != "" && !kept[r.ID] {
			m.Secrets.Delete(sessionSecretKey(r.ID))
		}
	}
}

// How often the loop looks at runtimes and balances: in the foreground,
// every loopTick, runtimes every second tick and balances every fourth;
// in the background, both at once every backgroundTick, to wake the
// device less.
var (
	loopTick       = 15 * time.Second
	backgroundTick = 3 * time.Minute
)

// SetBackground slows the loop down while the app is in the background,
// and brings it back, looking at once, when it isn't. Any thread.
func (m *Manager) SetBackground(background bool) {
	if m.work.background.Swap(background) == background {
		return
	}
	select {
	case m.work.wakeCh() <- struct{}{}:
	default:
	}
}

// peek runs fn on the main thread without a redraw, if the app can.
func (m *Manager) peek(fn func()) {
	if m.Peek != nil {
		m.Peek(fn)
		return
	}
	m.Post(fn)
}

// watched is a running session to look at, with its account.
type watched struct {
	s *Session
	a *Account
}

// due is what a tick of the loop looks at, on the main thread: the
// running sessions, with watch, and with balances the accounts, those
// with a live session in the background, all while any session is live.
// Without a live session, balances wait for the user to refresh them.
func (m *Manager) due(background, watch, balances bool) (accounts []*Account, sessions []watched) {
	live := map[string]bool{}
	for _, s := range m.Sessions {
		if s.Ended() {
			continue
		}
		live[s.Account] = true
		if a := m.Account(s.Account); watch && a != nil && s.Phase == PhaseRunning {
			sessions = append(sessions, watched{s, a})
		}
	}
	if !balances || len(live) == 0 {
		return nil, sessions
	}
	for _, a := range m.Accounts {
		if !background || live[a.Email] {
			accounts = append(accounts, a)
		}
	}
	return accounts, sessions
}

// loop watches balances and runtimes.
func (m *Manager) loop() {
	timer := time.NewTimer(loopTick)
	defer timer.Stop()
	n := 0
	for {
		now := false
		select {
		case <-m.ctx.Done():
			return
		case <-timer.C:
		case <-m.work.wakeCh():
			// Back in the foreground: look now. Into the background:
			// only wait longer.
			now = !m.work.background.Load()
			if !now {
				timer.Reset(backgroundTick)
				continue
			}
		}
		background := m.work.background.Load()
		if background {
			timer.Reset(backgroundTick)
		} else {
			timer.Reset(loopTick)
		}
		n++
		watch := background || now || n%2 == 0
		balances := background || now || n%4 == 0
		if !watch && !balances {
			continue
		}
		var accounts []*Account
		var sessions []watched
		done := make(chan struct{})
		m.peek(func() {
			accounts, sessions = m.due(background, watch, balances)
			close(done)
		})
		select {
		case <-done:
		case <-m.ctx.Done():
			return
		}
		for _, a := range accounts {
			go m.refreshAccount(a)
		}
		for _, w := range sessions {
			go m.watch(w.s, w.a)
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
