package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/plugins"
)

// What the settings' risky features refuse while they're off.
var (
	// ErrFreeWebUI is a task used through a web page refused on the free
	// tier, which Colab's FAQ restricts.
	ErrFreeWebUI = errors.New("on Colab's free tier, tasks used through a web page are off: turn them on under Settings → Risky features")
	// ErrPublicLinksOff is a public link asked for with them off.
	ErrPublicLinksOff = errors.New("public links are off: turn them on under Settings → Risky features")
	// ErrPublicLinkNeedsKey is a public link to an OpenAI API that takes
	// no API key, which anyone could use.
	ErrPublicLinkNeedsKey = errors.New("the API has no API key: set one and deploy again before making it public")
)

// grabMachine is the machine as grab asks for it: the wait between rounds
// no shorter than the settings allow.
func (m *Manager) grabMachine(machine Machine) Machine {
	machine.RetrySeconds = max(machine.RetrySeconds, m.Settings.MinRetry())
	return machine
}

// FreeTier reports whether the account has only Colab's free tier: no
// subscription, or no compute units left. False while its quota is unknown.
func (a *Account) FreeTier() bool {
	if a == nil || a.Quota == nil {
		return false
	}
	switch a.Quota.Tier {
	case "NONE", "FREE", "UNSPECIFIED":
		return true
	}
	return a.Quota.Balance <= 0
}

// portLister is a plugin that says which ports it serves before it runs.
type portLister interface{ Ports() []plugins.Port }

// WebTask reports whether a plugin is used through a web page: ComfyUI, or
// one that serves a port of kind web or comfyui. The llama plugin's OpenAI
// API isn't.
func WebTask(p plugins.Plugin) bool {
	if p == nil {
		return false
	}
	if p.ID() == "comfyui" {
		return true
	}
	var ports []plugins.Port
	switch x := p.(type) {
	case *plugins.ScriptPlugin:
		ports = x.Manifest.Ports
	case portLister:
		ports = x.Ports()
	}
	for _, port := range ports {
		if port.Kind == plugins.PortWeb || port.Kind == plugins.PortComfy {
			return true
		}
	}
	return false
}

// Shareable reports whether a port of this kind can have a public link.
func Shareable(k plugins.PortKind) bool {
	return k == plugins.PortWeb || k == plugins.PortOpenAI || k == plugins.PortComfy
}

// PublicLink is a port of a runtime reachable from the internet through a
// Cloudflare quick tunnel. It's kept nowhere: after a restart the tunnel
// is closed, and a new link has to be made.
type PublicLink struct {
	Remote int
	// URL is the link, "" until it's made.
	URL      string
	Starting bool
	Err      string
	cancel   context.CancelFunc
}

// Live reports whether the link is up, or on its way.
func (l *PublicLink) Live() bool { return l != nil && (l.Starting || l.URL != "") }

// PublicLink is the public link of a port of the session's runtime, nil
// for none.
func (s *Session) PublicLink(remote int) *PublicLink { return s.Public[remote] }

// HasPublicLink reports whether any port of the session is public, or
// about to be.
func (s *Session) HasPublicLink() bool {
	for _, l := range s.Public {
		if l.Live() {
			return true
		}
	}
	return false
}

// openShare starts the tunnel on the runtime; tests replace it.
var openShare = agent.OpenShare

// stopShare closes it.
var stopShare = agent.StopShare

// StartPublicLink makes a public link to a port of a running session, in
// the background; s.Public says how it goes. Call it from the main thread.
func (m *Manager) StartPublicLink(s *Session, remote int) error {
	if !m.Settings.Risky.PublicLinks {
		return ErrPublicLinksOff
	}
	if s.Phase != PhaseRunning {
		return errors.New("the runtime isn't running")
	}
	var port *plugins.Port
	for _, f := range s.Forwards {
		if f.Port.Remote == remote {
			p := f.Port
			port = &p
			break
		}
	}
	if port == nil {
		return fmt.Errorf("the runtime serves no port %d", remote)
	}
	if !Shareable(port.Kind) {
		return fmt.Errorf("port %d serves no web page or API", remote)
	}
	if port.Kind == plugins.PortOpenAI && (s.Result == nil || s.Result.APIKey == "") {
		return ErrPublicLinkNeedsKey
	}
	if s.Public[remote].Live() {
		return nil
	}
	a := m.Account(s.Account)
	if a == nil {
		return errors.New("no account")
	}
	ctx, cancel := context.WithCancel(m.ctx)
	l := &PublicLink{Remote: remote, Starting: true, cancel: cancel}
	if s.Public == nil {
		s.Public = map[int]*PublicLink{}
	}
	s.Public[remote] = l
	title := s.Title()
	m.logf(s, "Making a public link to port %d…", remote)
	go func() {
		defer cancel()
		sh, err := m.shellOf(ctx, s, a)
		url := ""
		if err == nil {
			url, err = openShare(ctx, sh, remote)
		}
		if ctx.Err() != nil {
			// Closed while it opened: make sure nothing is left.
			if sh != nil {
				sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
				stopShare(sctx, sh, remote)
				scancel()
			}
			return
		}
		if err != nil {
			m.logf(s, "✗ Public link to port %d: %s", remote, errText(err))
		} else {
			m.logf(s, "Public link to port %d: %s — anyone with it can reach the port.", remote, url)
		}
		m.Post(func() {
			if s.Public[remote] != l {
				return
			}
			l.Starting = false
			if err != nil {
				l.Err = errText(err)
				m.Notify("error", title, l.Err)
				return
			}
			l.URL = url
		})
	}()
	return nil
}

// StopPublicLink closes the public link of a port. Call it from the main
// thread.
func (m *Manager) StopPublicLink(s *Session, remote int) {
	l := s.Public[remote]
	if l == nil {
		return
	}
	delete(s.Public, remote)
	if l.cancel != nil {
		l.cancel()
	}
	a := m.Account(s.Account)
	if a == nil || s.Ended() {
		return
	}
	m.logf(s, "Closing the public link to port %d.", remote)
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		sh, err := m.shellOf(ctx, s, a)
		if err == nil {
			err = stopShare(ctx, sh, remote)
		}
		if err != nil {
			m.logf(s, "✗ Closing the public link to port %d: %s", remote, errText(err))
		}
	}()
}

// StopAllPublicLinks closes every session's public links, and disconnects
// their domains, as when the setting is turned off. Call it from the main
// thread.
func (m *Manager) StopAllPublicLinks() {
	for _, s := range m.Sessions {
		for _, remote := range publicPorts(s) {
			m.StopPublicLink(s, remote)
		}
	}
	m.StopAllDomainLinks()
}

// publicPorts are the ports of the session that have a public link.
func publicPorts(s *Session) []int {
	var out []int
	for remote := range s.Public {
		out = append(out, remote)
	}
	sort.Ints(out)
	return out
}

// dropPublicLinks forgets a session's public links, on the main thread,
// and returns their ports, for the runtime to close.
func (s *Session) dropPublicLinks() []int {
	ports := publicPorts(s)
	for _, l := range s.Public {
		if l.cancel != nil {
			l.cancel()
		}
	}
	s.Public = nil
	return ports
}

// closeShares closes tunnels on the runtime through the session's shell,
// if it has one; a runtime given back closes them anyway.
func (s *Session) closeShares(ports []int) {
	if len(ports) == 0 {
		return
	}
	s.mu.Lock()
	sh := s.shell
	s.mu.Unlock()
	if sh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, p := range ports {
		stopShare(ctx, sh, p)
	}
}

// leftShares are the tunnels still running on a runtime, as after the app
// restarted, which nothing here shows any more.
func leftShares(st *agent.Status) []string {
	if st == nil {
		return nil
	}
	var ids []string
	for id, p := range st.Plugins {
		if (strings.HasPrefix(id, agent.SharePrefix) || strings.HasPrefix(id, agent.DomainPrefix)) && p.Running {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
