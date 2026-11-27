package core

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"

	"golang.org/x/crypto/ssh"
)

// Use one ephemeral client identity for this app instance. The proxy receives
// its public key on every handshake, so reconnects need no on-disk private key.
func (m *Manager) sshSigner() (ssh.Signer, error) {
	m.sshKeyMu.Lock()
	defer m.sshKeyMu.Unlock()
	if m.sshKey != nil {
		return m.sshKey, nil
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, errors.New(i18n.T("transport.key_generate"))
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, errors.New(i18n.T("transport.key_generate"))
	}
	m.sshKey = signer
	return signer, nil
}

var errReplaced = errors.New("the session closed while a connection was opened")

func (s *Session) liveGeneration() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen, !s.stopping
}

func (s *Session) currentGeneration(gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen == gen
}

// lockTransport serializes physical connections. Callers carry their original
// generation through the wait, including SSH to mux handoffs and redials.
func (s *Session) lockTransport(ctx context.Context, gen int) (func(), error) {
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return nil, errReplaced
	}
	if s.transportGate == nil {
		s.transportGate = make(chan struct{}, 1)
	}
	gate := s.transportGate
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case gate <- struct{}{}:
	}
	unlock := func() { <-gate }
	if err := ctx.Err(); err != nil {
		unlock()
		return nil, err
	}
	if !s.currentGeneration(gen) {
		unlock()
		return nil, errReplaced
	}
	return unlock, nil
}

// openTransport prefers SSH. Once the proxy reports no SSH support, keep
// using the terminal until the session switches to a different runtime.
// The caller holds the transport gate.
func (m *Manager) openTransport(ctx context.Context, s *Session, conn *colab.Connection, gen int) (colab.Transport, error) {
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return nil, errReplaced
	}
	id := s.rid
	if s.transportID != id {
		s.transportID, s.hostKey, s.ttyStatus = id, nil, 0
		s.initialized, s.muxStarted = false, false
	}
	pin, ttyStatus, afterMux := s.hostKey, s.ttyStatus, s.muxStarted
	s.mu.Unlock()
	if ttyStatus == 0 {
		signer, err := m.sshSigner()
		if err != nil {
			return nil, err
		}
		sh, err := colab.DialSSH(ctx, conn, signer, colab.SSHOptions{HostKey: pin, HTTPClient: m.http})
		if err == nil {
			s.mu.Lock()
			stale := s.gen != gen || s.rid != id
			if !stale {
				s.hostKey = sh.HostKey()
			}
			s.mu.Unlock()
			if stale {
				sh.Close()
				return nil, errReplaced
			}
			if pin == nil {
				m.logf(s, "%s", i18n.T("transport.ssh"))
			}
			return sh, nil
		}
		var status *colab.SSHError
		if pin != nil || !errors.As(err, &status) || !status.Fallback() {
			return nil, err
		}
		s.mu.Lock()
		if s.gen != gen || s.rid != id {
			s.mu.Unlock()
			return nil, errReplaced
		}
		s.ttyStatus = status.Status
		s.mu.Unlock()
		m.logf(s, "%s", i18n.F("transport.tty", status.Status))
	}
	if afterMux {
		return colab.OpenShellAfterMux(ctx, conn)
	}
	return colab.OpenShell(ctx, conn)
}

// connect initializes a newly assigned or restored runtime. Reconnecting
// to the same runtime later leaves its detached agent and plugin alone.
func (m *Manager) connect(ctx context.Context, s *Session, a *Account, id string, conn *colab.Connection, gen int) (colab.Transport, *agent.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	unlock, err := s.lockTransport(ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	s.mu.Lock()
	sh, mux := s.shell, s.mux
	s.mu.Unlock()
	if mux != nil {
		if mux.Streams() > 0 {
			return nil, nil, errors.New(i18n.T("transport.tty_busy"))
		}
		mux.Close()
	}
	if !colab.Alive(sh) {
		sh, conn, err = m.openRuntimeTransport(ctx, s, a, id, conn, gen, 6)
		if err != nil {
			return nil, nil, err
		}
	}
	if err := agent.Install(ctx, sh); err != nil {
		sh.Close()
		return nil, nil, err
	}
	st, err := agent.GetStatus(ctx, sh)
	if err != nil {
		sh.Close()
		return nil, nil, err
	}
	if err := s.storeTransport(ctx, sh, conn, gen); err != nil {
		return nil, nil, err
	}
	m.observeSSHForwards(s, sh, gen)
	return sh, st, nil
}

// openRuntimeTransport refreshes proxy credentials at most once for a 401
// during this operation. Both attempts target the same assigned runtime.
func (m *Manager) openRuntimeTransport(ctx context.Context, s *Session, a *Account, id string, conn *colab.Connection, gen, attempts int) (colab.Transport, *colab.Connection, error) {
	refreshed := false
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		sh, err := m.openTransport(dialCtx, s, conn, gen)
		cancel()
		if err == nil {
			return sh, conn, nil
		}
		if proxyUnauthorized(err) {
			if refreshed || ctx.Err() != nil || !s.currentGeneration(gen) {
				return nil, nil, err
			}
			fresh, err := runtimeConnection(ctx, a.Client, id)
			if err != nil {
				return nil, nil, err
			}
			conn, refreshed = fresh, true
			continue
		}
		attempts--
		var status *colab.SSHError
		if attempts <= 0 || errors.Is(err, errReplaced) || errors.Is(err, colab.ErrSSHHostKey) ||
			(errors.As(err, &status) && status.Status < http.StatusInternalServerError) {
			return nil, nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func proxyUnauthorized(err error) bool {
	var api *colab.APIError
	return errors.Is(err, colab.ErrUnauthorized) || (errors.As(err, &api) && api.Status == http.StatusUnauthorized)
}

func (s *Session) storeTransport(ctx context.Context, sh colab.Transport, conn *colab.Connection, gen int) error {
	s.mu.Lock()
	if s.gen != gen || ctx.Err() != nil {
		s.mu.Unlock()
		sh.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errReplaced
	}
	old := s.shell
	s.shell, s.conn, s.initialized = sh, conn, true
	s.mu.Unlock()
	if old != nil && old != sh {
		old.Close()
	}
	return nil
}

// shellOf returns the session's shared control connection. The first caller
// after a drop fetches fresh proxy credentials and opens a replacement.
func (m *Manager) shellOf(ctx context.Context, s *Session, a *Account) (colab.Transport, error) {
	gen, live := s.liveGeneration()
	if !live || m.ctx.Err() != nil {
		return nil, errReplaced
	}
	return m.shellOfGeneration(ctx, s, a, gen)
}

func (m *Manager) shellOfGeneration(ctx context.Context, s *Session, a *Account, gen int) (colab.Transport, error) {
	unlock, err := s.lockTransport(ctx, gen)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return nil, errReplaced
	}
	sh, mux, id, initialized := s.shell, s.mux, s.rid, s.initialized
	s.mu.Unlock()
	if colab.Alive(sh) {
		return sh, nil
	}
	if id == "" {
		return nil, errors.New("the session has no runtime")
	}
	conn, err := runtimeConnection(ctx, a.Client, id)
	if err != nil {
		return nil, err
	}
	if mux != nil {
		// An active legacy mux owns the one shared terminal. Leave it in
		// place while it carries traffic or has open connections.
		if mux.Streams() > 0 {
			return nil, errors.New(i18n.T("transport.tty_busy"))
		}
		mux.Close()
	}
	sh, conn, err = m.openRuntimeTransport(ctx, s, a, id, conn, gen, 1)
	if err != nil {
		return nil, err
	}
	if !initialized {
		if err := agent.Install(ctx, sh); err != nil {
			sh.Close()
			return nil, err
		}
	}
	if err := s.storeTransport(ctx, sh, conn, gen); err != nil {
		return nil, err
	}
	m.observeSSHForwards(s, sh, gen)
	return sh, nil
}

// stream uses a separate SSH channel. A legacy control terminal must never
// race a second /colab/tty attachment for deployment monitoring.
func (m *Manager) stream(ctx context.Context, s *Session, a *Account, command, ready string) (plugins.StreamConn, *bufio.Reader, error) {
	sh, err := m.shellOf(ctx, s, a)
	if err != nil {
		return nil, nil, err
	}
	client, ok := sh.(*colab.SSH)
	if !ok {
		return nil, nil, errReplaced
	}
	stream, reader, err := client.Stream(ctx, command, ready)
	if err != nil {
		return nil, nil, err
	}
	return stream, reader, nil
}

func (m *Manager) dialForward(ctx context.Context, s *Session, a *Account, gen, port int) (io.ReadWriteCloser, error) {
	s.mu.Lock()
	legacy := s.ttyStatus != 0
	s.mu.Unlock()
	if !legacy {
		for attempt := range 2 {
			sh, err := m.shellOfGeneration(ctx, s, a, gen)
			if err != nil {
				return nil, err
			}
			client, ok := sh.(*colab.SSH)
			if !ok {
				break
			}
			conn, err := client.DialPort(ctx, port)
			var rejected *ssh.OpenChannelError
			if errors.As(err, &rejected) && rejected.Reason == ssh.Prohibited {
				return nil, fmt.Errorf("%s: %w", i18n.T("forward.ssh_prohibited"), err)
			}
			if attempt == 1 || !errors.Is(err, colab.ErrSSHClosed) || ctx.Err() != nil {
				return conn, err
			}
		}
	}
	unlock, err := s.lockTransport(ctx, gen)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s.mu.Lock()
	sh, mux, conn, id := s.shell, s.mux, s.conn, s.rid
	s.mu.Unlock()
	if mux != nil {
		select {
		case <-mux.Done():
		default:
			return mux.DialPort(ctx, port)
		}
	}
	if tty, ok := sh.(*colab.Shell); ok {
		// Finish the current command and detach before handing over the pane.
		if err := tty.Detach(ctx); err != nil {
			return nil, err
		}
	}
	refreshConnection := func() error {
		if !s.currentGeneration(gen) {
			return errReplaced
		}
		fresh, err := runtimeConnection(ctx, a.Client, id)
		if err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen != gen || s.rid != id {
			return errReplaced
		}
		s.conn = fresh
		return nil
	}
	if conn == nil || (!conn.ExpireTime.IsZero() && time.Until(conn.ExpireTime) < time.Minute) {
		if err := refreshConnection(); err != nil {
			return nil, err
		}
	}
	mux, err = s.dialMux(ctx, gen)
	if proxyUnauthorized(err) && ctx.Err() == nil {
		if err := refreshConnection(); err != nil {
			return nil, err
		}
		mux, err = s.dialMux(ctx, gen)
	}
	if err != nil {
		return nil, err
	}
	return mux.DialPort(ctx, port)
}

// sshTraffic keeps the idle watchdog informed now that SSH forwarding
// bypasses the agent mux. Only traffic counts, at most once per 10 seconds.
func (m *Manager) sshTraffic(s *Session) {
	s.mu.Lock()
	sh, ok := s.shell.(*colab.SSH)
	if !ok || time.Since(s.lastTraffic) < 10*time.Second {
		s.mu.Unlock()
		return
	}
	s.lastTraffic = time.Now()
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		sh.Run(ctx, "touch "+agent.Home+"/activity")
	}()
}
