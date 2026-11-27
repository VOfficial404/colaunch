package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
)

const runtimeLogBudget = 3 * time.Second

// Logs fetches the end of the session's plugin log from the runtime.
func (m *Manager) Logs(s *Session, done func(string, error)) {
	a := m.Account(s.Account)
	if a == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		out, proxyErr := runtimeLog(ctx, s, a)
		if proxyErr == nil {
			out = logTail(out, 300, 256<<10)
			m.Post(func() { done(out, nil) })
			return
		}
		sh, err := m.shellOf(ctx, s, a)
		if err == nil {
			out, err = agent.Logs(ctx, sh, s.Plugin, 300)
		}
		if err != nil {
			err = errors.Join(err, proxyErr)
		}
		m.Post(func() { done(out, err) })
	}()
}

// failDeploy takes a final snapshot while the runtime is still assigned.
// The session's in-memory log is also what the stopped-session view shows.
func (m *Manager) failDeploy(ctx context.Context, s *Session, a *Account, err error) {
	if ctx.Err() != nil {
		return
	}
	if isDeployTransportError(err) {
		out, logErr := runtimeLog(ctx, s, a)
		// A user stop owns the release if it canceled deployment while we read.
		if ctx.Err() != nil {
			return
		}
		if logErr != nil {
			m.logf(s, "✗ %s", logErr)
		} else if tail := logTail(out, 24, 16<<10); tail != "" {
			m.logf(s, "Installer log recovered from the runtime file API:")
			for line := range strings.SplitSeq(tail, "\n") {
				m.logf(s, "%s", line)
			}
		}
	}
	m.fail(s, a, err)
}

// These are the terminal and readiness monitor errors returned by Deploy.
func isDeployTransportError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, errReplaced) {
		return false
	}
	if errors.Is(err, colab.ErrMissingStart) || errors.Is(err, colab.ErrSSHClosed) {
		return true
	}
	msg := err.Error()
	switch msg {
	case "colab: the terminal command didn't start", "colab: the terminal closed",
		"invalid readiness probe response", "the readiness probe didn't answer":
		return true
	}
	return strings.HasPrefix(msg, "opening a terminal to wait on: ") ||
		strings.HasPrefix(msg, "the readiness probe exited with status ") ||
		strings.HasPrefix(msg, "colab: terminal: ") ||
		strings.HasPrefix(msg, "colab: the terminal closed: ") ||
		strings.HasPrefix(msg, "colab: the terminal command ended before it was ready: ")
}

func runtimeLog(ctx context.Context, s *Session, a *Account) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimeLogBudget)
	defer cancel()
	s.mu.Lock()
	conn, id := s.conn, s.rid
	s.mu.Unlock()
	if id == "" {
		return "", errors.New("colab: plugin log via Jupyter Contents: the session has no runtime")
	}
	if conn == nil || (!conn.ExpireTime.IsZero() && time.Now().After(conn.ExpireTime)) {
		// Fetch only connection metadata for the existing runtime. This also
		// works for a restored session whose command terminal never opened.
		var err error
		conn, err = runtimeConnection(ctx, a.Client, id)
		if err != nil {
			return "", errors.New("colab: plugin log via Jupyter Contents: runtime proxy connection unavailable")
		}
	}
	return a.Client.PluginLog(ctx, conn, s.Plugin)
}

// runtimeWaitStatus reads one launch's state over HTTPS, without starting
// a terminal or assigning a runtime.
func runtimeWaitStatus(ctx context.Context, s *Session, a *Account, plugin, nonce string) (colab.WaitStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.mu.Lock()
	old, id, gen := s.conn, s.rid, s.gen
	s.mu.Unlock()
	if id == "" {
		return colab.WaitStatus{}, errors.New("the session has no runtime")
	}
	if old != nil && (old.ExpireTime.IsZero() || time.Now().Before(old.ExpireTime)) {
		status, err := a.Client.PluginWaitStatus(ctx, old, plugin, nonce)
		if !errors.Is(err, colab.ErrRuntimeProxyAuth) {
			return status, err
		}
	}
	fresh, err := runtimeConnection(ctx, a.Client, id)
	if err != nil {
		return colab.WaitStatus{}, errors.New("deployment status: could not refresh the runtime connection")
	}
	s.mu.Lock()
	if s.gen != gen || s.rid != id || ctx.Err() != nil {
		s.mu.Unlock()
		return colab.WaitStatus{}, errReplaced
	}
	if s.conn == old {
		s.conn = fresh
	}
	s.mu.Unlock()
	return a.Client.PluginWaitStatus(ctx, fresh, plugin, nonce)
}

// logTail bounds the displayed log and keeps the final redraw of each line.
func logTail(log string, lines, maxBytes int) string {
	log = strings.TrimRight(log, "\n")
	if len(log) > maxBytes {
		start := len(log) - maxBytes
		partial := log[start-1] != '\n'
		log = log[start:]
		if i := strings.IndexByte(log, '\n'); partial && i >= 0 {
			log = log[i+1:]
		}
	}
	for end := len(log); lines > 0; lines-- {
		end = strings.LastIndexByte(log[:end], '\n')
		if end < 0 {
			break
		}
		if lines == 1 {
			log = log[end+1:]
		}
	}
	out := strings.Split(strings.ToValidUTF8(log, "\uFFFD"), "\n")
	for i, line := range out {
		line = strings.TrimSuffix(line, "\r")
		out[i] = line[strings.LastIndexByte(line, '\r')+1:]
	}
	return strings.Join(out, "\n")
}
