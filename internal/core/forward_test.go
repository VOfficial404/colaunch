package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/plugins"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

func forwardPosts(t *testing.T, m *Manager) func(func() bool) {
	t.Helper()
	posts := make(chan func(), 100)
	m.Post = func(fn func()) { posts <- fn }
	return func(ready func() bool) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for !ready() {
			select {
			case fn := <-posts:
				fn()
			case <-timer.C:
				t.Fatal("timed out waiting for forward state")
			}
		}
	}
}

func forwardProxy(t *testing.T, dial func(ssh.NewChannel)) *httptest.Server {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(host)
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/colab/ssh" {
			t.Errorf("unexpected proxy path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		c, channels, requests, err := ssh.NewServerConn(nc, cfg)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		go ssh.DiscardRequests(requests)
		for ch := range channels {
			go dial(ch)
		}
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

func sendForward(t *testing.T, ch ssh.NewChannel, target string) {
	t.Helper()
	var p struct {
		Host       string
		Port       uint32
		Origin     string
		OriginPort uint32
	}
	if ch.ChannelType() != "direct-tcpip" || ssh.Unmarshal(ch.ExtraData(), &p) != nil || p.Host != "127.0.0.1" || p.Port != 8188 {
		t.Errorf("unexpected remote channel %s %+v", ch.ChannelType(), p)
		ch.Reject(ssh.UnknownChannelType, "wrong destination")
		return
	}
	rc, err := net.DialTimeout("tcp", target, time.Second)
	if err != nil {
		ch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	defer rc.Close()
	stream, requests, err := ch.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	go ssh.DiscardRequests(requests)
	go func() {
		io.Copy(rc, stream)
		rc.(*net.TCPConn).CloseWrite()
	}()
	io.Copy(stream, rc)
}

func forwardRuntimeResponse(r *http.Request, c *colab.Connection) *http.Response {
	data, _ := json.Marshal(colab.Runtime{Name: "runtimes/test-runtime", Connection: c})
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Request: r}
}

func TestComfyForwardRefusalAndReconnect(t *testing.T) {
	var policyBlocked atomic.Bool
	policyBlocked.Store(true)
	var backendCalls atomic.Int32
	var validStats atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/system_stats" {
			t.Errorf("health request was %s %s", r.Method, r.URL.Path)
		}
		backendCalls.Add(1)
		if validStats.Load() {
			fmt.Fprint(w, `{"system":{"os":"test"},"devices":[]}`)
		} else {
			fmt.Fprint(w, `{"unrelated":true}`)
		}
	}))
	defer backend.Close()
	proxy := forwardProxy(t, func(ch ssh.NewChannel) {
		if ch.ChannelType() == "session" {
			ch.Reject(ssh.Prohibited, "watchdog ping not needed by this test")
			return
		}
		if policyBlocked.Load() {
			ch.Reject(ssh.Prohibited, "forwarding denied by policy")
			return
		}
		sendForward(t, ch, backend.Listener.Addr().String())
	})
	conn := &colab.Connection{URL: proxy.URL}
	var lookups, deletes atomic.Int32
	a := acct("a@example.com")
	a.Client = &colab.Client{
		Token: func(context.Context) (string, error) { return "access-token", nil },
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodDelete {
				deletes.Add(1)
			}
			lookups.Add(1)
			return forwardRuntimeResponse(r, conn), nil
		})},
	}
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	m.http = proxy.Client()
	wait := forwardPosts(t, m)
	p := plugins.Port{Name: "ComfyUI", Remote: 8188, Kind: plugins.PortComfy, Path: "/"}
	s := &Session{rid: "test-runtime", Phase: PhaseRunning, announceComfy: true}
	m.Sessions = []*Session{s}
	readyNotices := 0
	m.Notify = func(kind, _, _ string) {
		if kind == "success" {
			readyNotices++
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first, err := m.openTransport(ctx, s, conn, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.storeTransport(ctx, first, conn, 0); err != nil {
		t.Fatal(err)
	}
	m.observeSSHForwards(s, first, 0)
	m.forwardAll(s, a, 0, []plugins.Port{p}, nil)
	wait(func() bool { return len(s.Forwards) == 1 })
	f := s.Forwards[0]
	addr := f.ListeningAddress()
	if addr == "" || !f.Checking || f.Address() != "" {
		t.Fatalf("listener displayed as ready before probe: %+v", f)
	}
	wait(func() bool { return !f.Checking })
	if f.URL() != "" || !strings.Contains(f.CheckErr, "direct-tcpip") || !strings.Contains(f.CheckErr, "denied by policy") {
		t.Fatalf("remote refusal was not reported: %+v", f)
	}
	if !colab.Alive(first) || backendCalls.Load() != 0 || s.Phase != PhaseRunning || readyNotices != 0 || deletes.Load() != 0 {
		t.Fatal("channel refusal changed the runtime or announced readiness")
	}

	policyBlocked.Store(false)
	m.RetryForward(s, f)
	wait(func() bool { return !f.Checking })
	if f.Verified || f.CheckErr == "" || backendCalls.Load() != 1 {
		t.Fatalf("non-ComfyUI HTTP 200 was accepted: %+v", f)
	}
	validStats.Store(true)
	m.RetryForward(s, f)
	wait(func() bool { return f.Verified })
	if f.Address() != addr || readyNotices != 1 || backendCalls.Load() != 2 {
		t.Fatalf("valid stats did not verify the original listener: %+v", f)
	}

	first.Close()
	wait(func() bool { return !f.Verified })
	if f.Address() != "" || f.CheckErr == "" {
		t.Fatalf("SSH drop left the endpoint advertised: %+v", f)
	}
	// A control reconnect (for example, the runtime watcher) rechecks the
	// same listener without waiting for the user to click Retry.
	second, err := m.shellOf(ctx, s, a)
	if err != nil || second == first {
		t.Fatalf("couldn't open a replacement SSH connection: %v", err)
	}
	wait(func() bool { return f.Verified })
	if f.Address() != addr || lookups.Load() != 1 || readyNotices != 1 || backendCalls.Load() != 3 || deletes.Load() != 0 {
		t.Fatalf("replacement SSH did not reverify the original listener: %+v", f)
	}
}

func TestStopForwardAfterDeleteFailure(t *testing.T) {
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	wait := forwardPosts(t, m)
	p := plugins.Port{Name: "Extra port", Remote: 8188, Kind: plugins.PortTCP}
	s := &Session{rid: "test-runtime", Phase: PhaseRunning}
	s.RuntimeID, s.Extra = s.rid, []plugins.Port{p}
	m.Sessions = []*Session{s}
	a := acct("a@example.com")
	s.Account, m.Accounts = a.Email, []*Account{a}
	m.forwardAll(s, a, 0, nil, s.Extra)
	wait(func() bool { return len(s.Forwards) == 1 })
	old, addr := s.Forwards[0], s.Forwards[0].Address()
	var deletes atomic.Int32
	a.Client = &colab.Client{
		Token: func(context.Context) (string, error) { return "access-token", nil },
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			status := http.StatusOK
			if r.Method == http.MethodDelete && deletes.Add(1) == 1 {
				if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
					c.Close()
					t.Error("old listener still accepted connections when DELETE started")
				}
				status = http.StatusServiceUnavailable
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
		})},
	}

	m.Stop(s, "user")
	if s.Phase != PhaseStopping || len(s.Forwards) != 0 {
		t.Fatal("stop left forward state visible")
	}
	// Queued work for the previous forward must not install another listener.
	m.forwardAll(s, a, 0, nil, s.Extra)
	m.phase(s, 0, PhaseForwarding)
	wait(func() bool { return s.Phase == PhaseRunning && len(s.Forwards) == 1 })
	if s.Forwards[0] == old || s.Forwards[0].Address() == "" || s.Err == "" || deletes.Load() != 1 {
		t.Fatalf("failed stop did not replace the closed listener: %+v", s.Forwards)
	}
	addr2 := s.Forwards[0].Address()
	m.Stop(s, "user")
	wait(func() bool { return s.Phase == PhaseStopped })
	if len(s.Forwards) != 0 || deletes.Load() != 2 {
		t.Fatal("the retry left forwards behind")
	}
	if c, err := net.DialTimeout("tcp", addr2, 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("rebuilt forward still accepted connections after stop")
	}
}
