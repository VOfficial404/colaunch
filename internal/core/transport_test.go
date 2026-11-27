package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"colaunch/internal/colab"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

func TestTransportFallback(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var sshDials, ttyDials atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/colab/ssh":
					sshDials.Add(1)
					w.WriteHeader(status)
				case "/colab/tty":
					ttyDials.Add(1)
					ws, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer ws.CloseNow()
					for {
						_, msg, err := ws.Read(r.Context())
						if err != nil {
							return
						}
						var frame struct{ Data string }
						if json.Unmarshal(msg, &frame) == nil {
							if marker, ok := strings.CutPrefix(frame.Data, `echo @@H""ELLO-`); ok {
								answer, _ := json.Marshal(map[string]string{"data": "@@HELLO-" + marker})
								ws.Write(r.Context(), websocket.MessageText, answer)
							}
						}
					}
				default:
					t.Errorf("unexpected proxy path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer proxy.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			m := New(t.TempDir(), mapSecrets{})
			defer m.Close()
			s := &Session{rid: "test-runtime"}
			defer s.closeConns()
			proxyConn := &colab.Connection{URL: proxy.URL}
			sh, err := m.openTransport(ctx, s, proxyConn, 0)
			if status == http.StatusTooManyRequests {
				var upgrade *colab.SSHError
				if !errors.As(err, &upgrade) || upgrade.Status != status || ttyDials.Load() != 0 {
					t.Fatalf("429 opened %d fallback terminals, err = %v", ttyDials.Load(), err)
				}
				return
			}
			if _, ok := sh.(*colab.Shell); !ok || err != nil {
				t.Fatalf("404 fallback = %T, %v", sh, err)
			}
			if err := s.storeTransport(ctx, sh, proxyConn, 0); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.stream(ctx, s, nil, "monitor", "ready"); err == nil || ttyDials.Load() != 1 || sshDials.Load() != 1 {
				t.Fatalf("legacy monitor opened %d terminals and %d SSH connections, err = %v", ttyDials.Load(), sshDials.Load(), err)
			}
		})
	}
}

func TestSSHReconnectSharesFreshDial(t *testing.T) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int32
	upgraded := make(chan net.Conn, 8)
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantTokens := []string{"initial-expired", "first-token", "replacement-expired", "renewed-token"}
		i := int(dials.Add(1)) - 1
		if i >= len(wantTokens) || r.Header.Get("X-Colab-Runtime-Proxy-Token") != wantTokens[i] {
			t.Error("SSH dial used an unexpected proxy credential")
			http.Error(w, "wrong credential", http.StatusForbidden)
			return
		}
		if i == 0 || i == 2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(r.Header.Get("X-Colab-Ssh-Pubkey")))
		if err != nil {
			t.Error(err)
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		upgraded <- nc
		cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, pub ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), pub.Marshal()) {
				return nil, errors.New("wrong key")
			}
			return nil, nil
		}}
		cfg.AddHostKey(host)
		sshConn, channels, requests, err := ssh.NewServerConn(nc, cfg)
		if err != nil {
			t.Error(err)
			return
		}
		defer sshConn.Close()
		go ssh.DiscardRequests(requests)
		for ch := range channels {
			ch.Reject(ssh.Prohibited, "no channel needed")
		}
	}))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := mapSecrets{}
	m := New(t.TempDir(), store)
	defer m.Close()
	m.http = proxy.Client()
	initial := &colab.Connection{URL: proxy.URL, Token: "initial-expired"}
	s := &Session{rid: "test-runtime", conn: initial}
	defer s.closeConns()
	var lookups atomic.Int32
	a := &Account{Client: &colab.Client{
		Token: func(context.Context) (string, error) { return "access-token", nil },
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/runtimes/test-runtime") {
				t.Errorf("credential refresh changed runtime: %s %s", r.Method, r.URL.Path)
			}
			tokens := []string{"first-token", "replacement-expired", "renewed-token"}
			i := int(lookups.Add(1)) - 1
			if i >= len(tokens) {
				return nil, errors.New("excess credential lookup")
			}
			return forwardRuntimeResponse(r, &colab.Connection{URL: proxy.URL, Token: tokens[i]}), nil
		})},
	}}
	first, refreshed, err := m.openRuntimeTransport(ctx, s, a, s.rid, initial, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Token != "first-token" {
		t.Fatal("initial connection did not retain the refreshed credentials")
	}
	if err := s.storeTransport(ctx, first, refreshed, 0); err != nil {
		t.Fatal(err)
	}

	(<-upgraded).Close()
	select {
	case <-first.Done():
	case <-ctx.Done():
		t.Fatal("SSH drop was not detected")
	}
	var wg sync.WaitGroup
	var got [4]colab.Transport
	for i := range got {
		wg.Go(func() {
			sh, err := m.shellOf(ctx, s, a)
			if err != nil {
				t.Error(err)
			}
			got[i] = sh
		})
	}
	wg.Wait()
	for _, sh := range got {
		if sh == nil || sh == first || sh != got[0] {
			t.Fatal("parallel operations did not reuse the replacement SSH connection")
		}
	}
	if lookups.Load() != 3 || dials.Load() != 4 {
		t.Fatalf("reconnect made %d API calls and %d total SSH connections", lookups.Load(), dials.Load())
	}

	other := New(t.TempDir(), store)
	defer other.Close()
	otherSigner, err := other.sshSigner()
	if err != nil || bytes.Equal(otherSigner.PublicKey().Marshal(), m.sshKey.PublicKey().Marshal()) || len(store) != 0 {
		t.Fatal("app instances should use distinct SSH keys without writing them to Secrets")
	}
}

func TestSSHUnauthorizedRefreshOnce(t *testing.T) {
	var dials, lookups atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/colab/ssh" {
			t.Errorf("401 triggered another transport: %s", r.URL.Path)
		}
		dials.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer proxy.Close()
	conn := &colab.Connection{URL: proxy.URL}
	a := &Account{Client: &colab.Client{
		Token: func(context.Context) (string, error) { return "access-token", nil },
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			lookups.Add(1)
			return forwardRuntimeResponse(r, conn), nil
		})},
	}}
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	s := &Session{rid: "test-runtime"}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, _, err := m.openRuntimeTransport(ctx, s, a, s.rid, conn, 0, 6)
	if !errors.Is(err, colab.ErrUnauthorized) || dials.Load() != 2 || lookups.Load() != 1 {
		t.Fatalf("persistent 401 made %d SSH dials and %d lookups: %v", dials.Load(), lookups.Load(), err)
	}
}
