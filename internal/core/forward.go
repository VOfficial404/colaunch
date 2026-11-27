package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/comfy"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
	"colaunch/internal/tunnel"
)

const (
	comfyDialTimeout  = 25 * time.Second
	comfyCheckTimeout = 30 * time.Second
)

var localForwardHTTP = &http.Client{
	Transport: &http.Transport{DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// ForwardState is a port forwarded to 127.0.0.1. Health changes on the UI thread.
type ForwardState struct {
	Port  plugins.Port
	Extra bool
	Err   string // listener error

	Checking bool
	Verified bool
	CheckErr string

	f          *tunnel.Forward
	checkedSSH *colab.SSH
}

// ListeningAddress is where the local listener accepted connections.
func (f *ForwardState) ListeningAddress() string {
	if f.f == nil {
		return ""
	}
	return f.f.Local
}

// Address is the local service address once its required check has passed.
func (f *ForwardState) Address() string {
	if f.Port.Kind == plugins.PortComfy && (!f.Verified || (f.checkedSSH != nil && !colab.Alive(f.checkedSSH))) {
		return ""
	}
	return f.ListeningAddress()
}

// Conns is how many connections are open through the forward.
func (f *ForwardState) Conns() int {
	if f.f == nil {
		return 0
	}
	return f.f.Conns()
}

// URL is the forward's address as a link.
func (f *ForwardState) URL() string {
	if address := f.Address(); address != "" {
		return "http://" + address + f.Port.Path
	}
	return ""
}

// forwardAll opens local listeners. A ComfyUI listener is checked separately
// so a blocked remote port does not delay or fail runtime deployment.
func (m *Manager) forwardAll(s *Session, a *Account, gen int, ports, extra []plugins.Port) {
	dial := tunnel.ForwardDialFunc(func(ctx context.Context, port int) (io.ReadWriteCloser, error) {
		conn, err := m.dialForward(ctx, s, a, gen, port)
		if err == nil && !s.currentGeneration(gen) {
			conn.Close()
			return nil, errReplaced
		}
		return conn, err
	})
	var states []*ForwardState
	for i, p := range append(slices.Clone(ports), extra...) {
		if !s.currentGeneration(gen) || m.ctx.Err() != nil {
			return
		}
		fs := &ForwardState{Port: p, Extra: i >= len(ports)}
		local := fmt.Sprintf("127.0.0.1:%d", p.Local)
		opts := tunnel.ForwardOptions{
			OnDialError: func(err error) {
				m.logf(s, "%s", i18n.F("transport.forward_error", p.Remote, errText(err)))
				if p.Kind == plugins.PortComfy {
					m.Post(func() {
						if m.hasForward(s, fs, gen) && fs.Verified && fs.f.LastDialError() != nil {
							fs.Verified, fs.CheckErr = false, errText(fs.f.LastDialError())
						}
					})
				}
			},
			OnTraffic: func() { m.sshTraffic(s) },
		}
		if p.Kind == plugins.PortComfy {
			opts.DialTimeout = comfyDialTimeout
		}
		f, err := tunnel.Listen(local, p.Remote, dial, opts)
		if err != nil {
			m.logf(s, "%s", i18n.F("forward.local_busy", p.Local))
			f, err = tunnel.Listen("127.0.0.1:0", p.Remote, dial, opts)
		}
		if err != nil {
			fs.Err = errText(err)
		} else {
			fs.f = f
			s.mu.Lock()
			if s.gen != gen || s.stopping || m.ctx.Err() != nil {
				s.mu.Unlock()
				f.Close()
				return
			}
			s.fwds = append(s.fwds, f)
			s.mu.Unlock()
			m.logf(s, "%s", i18n.F("forward.listening", f.Local, p.Remote, p.Name))
		}
		states = append(states, fs)
	}
	m.Post(func() {
		if !s.currentGeneration(gen) || m.ctx.Err() != nil || s.Ended() || s.Phase == PhaseStopping {
			return
		}
		s.Forwards = append(s.Forwards, states...)
		for _, f := range states {
			m.checkForward(s, f, gen)
		}
	})
}

// restoreForwards rebuilds the listeners for a runtime that already exists.
func (m *Manager) restoreForwards(s *Session, a *Account, gen int) {
	if a == nil || len(s.Forwards) != 0 || s.rebuildingForwards {
		return
	}
	var ports []plugins.Port
	if s.Result != nil {
		ports = s.Result.Ports
	}
	extra := slices.Clone(s.Extra)
	if len(ports)+len(extra) == 0 {
		return
	}
	s.rebuildingForwards = true
	go func() {
		m.forwardAll(s, a, gen, ports, extra)
		m.Post(func() {
			if s.currentGeneration(gen) {
				s.rebuildingForwards = false
			}
		})
	}()
}

// RetryForward checks the existing listener again, reconnecting the remote
// transport if needed when the request reaches the listener.
func (m *Manager) RetryForward(s *Session, f *ForwardState) {
	if s.Phase == PhaseRunning {
		if gen, live := s.liveGeneration(); live && m.hasForward(s, f, gen) {
			m.checkForward(s, f, gen)
		}
	}
}

func (m *Manager) hasForward(s *Session, f *ForwardState, gen int) bool {
	return m.ctx.Err() == nil && s.currentGeneration(gen) && slices.Contains(s.Forwards, f)
}

func (m *Manager) checkForward(s *Session, f *ForwardState, gen int) {
	if f.Port.Kind != plugins.PortComfy || f.f == nil || f.Checking {
		return
	}
	f.Checking, f.Verified, f.CheckErr = true, false, ""
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, comfyCheckTimeout)
		defer cancel()
		client := comfy.New("http://" + f.f.Local)
		client.HTTP = localForwardHTTP
		clientSSH := s.forwardSSH()
		err := client.Ready(ctx)
		if replacement := s.forwardSSH(); replacement != clientSSH {
			// The request may have opened the replacement itself. Check on
			// that connection within the same deadline before crediting it.
			clientSSH = replacement
			if err == nil {
				err = client.Ready(ctx)
			}
		}
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			// A channel refusal resets the local HTTP connection. Keep the
			// remote dial error so the user can see why it reset.
			if dialErr := f.f.LastDialError(); dialErr != nil {
				err = dialErr
			}
		}
		m.Post(func() {
			if !m.hasForward(s, f, gen) {
				return
			}
			f.Checking, f.checkedSSH = false, clientSSH
			if replacement := s.forwardSSH(); replacement != clientSSH && replacement != nil && colab.Alive(replacement) {
				m.checkForward(s, f, gen)
				return
			}
			f.CheckErr = errText(err)
			if err == nil && clientSSH != nil && !colab.Alive(clientSSH) {
				f.CheckErr = i18n.T("forward.ssh_disconnected")
			}
			if f.CheckErr != "" {
				m.logf(s, "%s", i18n.F("forward.check_failed", f.f.Local, f.CheckErr))
				return
			}
			f.Verified = true
			m.logf(s, "%s", i18n.F("forward.check_ready", f.f.Local))
			if s.announceComfy {
				s.announceComfy = false
				m.Notify("success", s.Title(), f.URL())
				m.alertReady(s)
			}
		})
	}()
}

func (s *Session) forwardSSH() *colab.SSH {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, _ := s.shell.(*colab.SSH)
	return sh
}

// observeSSHForwards invalidates a successful check when its SSH connection
// ends, and checks again when the session opens a replacement connection.
func (m *Manager) observeSSHForwards(s *Session, transport colab.Transport, gen int) {
	sh, ok := transport.(*colab.SSH)
	if !ok {
		return
	}
	m.Post(func() {
		if !s.currentGeneration(gen) || m.ctx.Err() != nil {
			return
		}
		for _, f := range s.Forwards {
			if f.checkedSSH != sh {
				m.checkForward(s, f, gen)
			}
		}
	})
	go func() {
		select {
		case <-m.ctx.Done():
			return
		case <-sh.Done():
		}
		m.Post(func() {
			if !s.currentGeneration(gen) || m.ctx.Err() != nil {
				return
			}
			for _, f := range s.Forwards {
				if f.checkedSSH == sh && f.Verified {
					f.Verified, f.CheckErr = false, i18n.T("forward.ssh_disconnected")
				}
			}
		})
	}()
}

// AddForward forwards another port of a running session's runtime.
func (m *Manager) AddForward(s *Session, remote, local int, name string) {
	gen, live := s.liveGeneration()
	if !live || s.Phase != PhaseRunning {
		return
	}
	p := plugins.Port{Name: name, Remote: remote, Local: local, Kind: plugins.PortTCP}
	if name == "" {
		p.Name = fmt.Sprintf("port %d", remote)
	}
	s.Extra = append(s.Extra, p)
	m.saveState()
	if a := m.Account(s.Account); a != nil {
		go m.forwardAll(s, a, gen, nil, []plugins.Port{p})
	}
}

// RemoveForward stops a forward the user added.
func (m *Manager) RemoveForward(s *Session, f *ForwardState) {
	i := slices.Index(s.Forwards, f)
	if i < 0 {
		return
	}
	s.Forwards = slices.Delete(s.Forwards, i, i+1)
	if f.f != nil {
		s.mu.Lock()
		if i := slices.Index(s.fwds, f.f); i >= 0 {
			s.fwds = slices.Delete(s.fwds, i, i+1)
		}
		s.mu.Unlock()
		f.f.Close()
	}
	if i := slices.Index(s.Extra, f.Port); i >= 0 {
		s.Extra = slices.Delete(s.Extra, i, i+1)
	}
	m.saveState()
}
