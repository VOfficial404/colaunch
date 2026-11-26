package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"colaunch/internal/colab"
)

const testRuntimeLogPath = "colaunch/plugins/comfyui/log.txt"

func serveRuntimeLog(t *testing.T, w http.ResponseWriter, r *http.Request, log string) {
	t.Helper()
	if r.Method != http.MethodGet || r.URL.Path != "/api/contents/"+testRuntimeLogPath ||
		r.Header.Get("X-Colab-Runtime-Proxy-Token") != "runtime-token" || r.Header.Get("Authorization") != "" {
		t.Error("unexpected Contents request")
	}
	json.NewEncoder(w).Encode(map[string]string{"path": testRuntimeLogPath, "type": "file", "format": "text", "content": log})
}

func TestDeployFailureRecoversLogBeforeRelease(t *testing.T) {
	monitorErr := errors.New("opening a terminal to wait on: context deadline exceeded\ncolab: the terminal command didn't start")
	for _, tc := range []struct {
		name          string
		err           error
		missing, keep bool
		stopRead      bool
		wantRequests  string
		wantLog       string
	}{
		{name: "PTYs unavailable", err: monitorErr, wantRequests: "contents release", wantLog: "ComfyUI startup traceback"},
		{name: "PTY diagnostics", err: errors.New("opening a terminal to wait on: terminal transport failed (monitor terminal: closed, command terminal: unavailable, elapsed: 30s)"), wantRequests: "contents release", wantLog: "ComfyUI startup traceback"},
		{name: "relative path unavailable", err: monitorErr, missing: true, wantRequests: "contents release", wantLog: "relative log file unavailable (HTTP 404)"},
		{name: "unrelated plugin failure", err: errors.New("the plugin stopped: opening a terminal to wait on: diagnostic text"), wantRequests: "release"},
		{name: "parent canceled during read", err: monitorErr, stopRead: true, wantRequests: "contents"},
		{name: "kept runtime", err: monitorErr, keep: true, wantRequests: "contents", wantLog: "ComfyUI startup traceback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.missing {
					http.NotFound(w, r)
					return
				}
				serveRuntimeLog(t, w, r, "discarded early line\n"+strings.Repeat("setup\n", 30)+"old progress\rComfyUI startup traceback\nruntime-token\n")
			}))
			defer srv.Close()
			var requests []string
			client := &colab.Client{
				Token: func(context.Context) (string, error) { return "fake-access-token", nil },
				HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
					if "https://"+r.URL.Host == srv.URL {
						requests = append(requests, "contents")
						if tc.stopRead {
							cancel()
							return nil, r.Context().Err()
						}
						return srv.Client().Transport.RoundTrip(r)
					}
					if r.Method != http.MethodDelete || r.URL.Path != "/v1beta/runtimes/rt-test" || r.Context().Err() != nil {
						t.Error("expected to release the same runtime with a live context")
					}
					requests = append(requests, "release")
					return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
				})},
			}
			m := New(t.TempDir(), mapSecrets{})
			defer m.Close()
			m.Settings.KeepFailed = tc.keep
			s := &Session{sessionRecord: sessionRecord{Plugin: "comfyui"}, rid: "rt-test", conn: &colab.Connection{URL: srv.URL, Token: "runtime-token"}}
			// Drain posted work on this thread, including the batched log update.
			posts := make(chan func(), 8)
			m.Post = func(fn func()) { posts <- fn }
			m.failDeploy(ctx, s, &Account{Client: client}, tc.err)
			m.flushLogs()
			for len(posts) > 0 {
				(<-posts)()
			}
			if got := strings.Join(requests, " "); got != tc.wantRequests {
				t.Errorf("requests = %q, want %q", got, tc.wantRequests)
			}
			wantPhase, wantErr := PhaseFailed, tc.err.Error()
			if tc.keep {
				wantPhase = PhaseRunning
			}
			if tc.stopRead {
				wantPhase, wantErr = "", ""
			}
			if s.Phase != wantPhase || s.Err != wantErr {
				t.Errorf("got phase %q and failure %q", s.Phase, s.Err)
			}
			var logs strings.Builder
			for _, line := range s.Log {
				logs.WriteString(line.Text + "\n")
			}
			out := logs.String()
			if (tc.wantLog != "" && !strings.Contains(out, tc.wantLog)) || strings.Contains(out, "runtime-token") ||
				strings.Contains(out, "discarded early line") || strings.Contains(out, "old progress") {
				t.Errorf("unexpected diagnostics: %q", out)
			}
		})
	}
}

func TestManagerLogsUsesContentsWithoutShell(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveRuntimeLog(t, w, r, "first line\n"+strings.Repeat("previous\n", 299)+"starting\rready\r\n")
	}))
	defer srv.Close()
	client := &colab.Client{
		Token: func(context.Context) (string, error) { return "fake-access-token", nil },
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if "https://"+r.URL.Host == srv.URL {
				return srv.Client().Transport.RoundTrip(r)
			}
			if r.Method != http.MethodGet || r.URL.Path != "/v1beta/runtimes/rt-test" {
				return nil, errors.New("unexpected runtime API request")
			}
			body, _ := json.Marshal(colab.Runtime{Connection: &colab.Connection{URL: srv.URL, Token: "runtime-token"}})
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})},
	}
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	a := acct("owner@example.test")
	a.Client = client
	m.Accounts = []*Account{a}
	// A restored session can have a runtime ID without any open terminal.
	s := &Session{sessionRecord: sessionRecord{Plugin: "comfyui", Account: a.Email}, rid: "rt-test"}
	assertRemoteLog(t, m, s, strings.Repeat("previous\n", 299)+"ready")
}

func TestManagerLogsFallsBackToNormalShell(t *testing.T) {
	proxy := httptest.NewTLSServer(http.NotFoundHandler())
	defer proxy.Close()
	terminal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			var msg struct{ Data string }
			if wsjson.Read(r.Context(), ws, &msg) != nil {
				return
			}
			var reply string
			switch {
			case strings.HasPrefix(msg.Data, `echo @@H""ELLO-`):
				reply = strings.Replace(strings.TrimPrefix(msg.Data, "echo "), `""`, "", 1)
			case strings.HasPrefix(msg.Data, `echo @@S""TART-1@@`):
				reply = "@@START-1@@\nlog from the command terminal\n@@END-1:0@@\n"
			}
			if reply != "" && wsjson.Write(r.Context(), ws, map[string]string{"data": reply}) != nil {
				return
			}
		}
	}))
	defer terminal.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	sh, err := colab.OpenShell(ctx, &colab.Connection{URL: terminal.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	a := acct("owner@example.test")
	a.Client = &colab.Client{HTTP: proxy.Client()}
	m.Accounts = []*Account{a}
	s := &Session{sessionRecord: sessionRecord{Plugin: "comfyui", Account: a.Email}, rid: "rt-test", shell: sh,
		conn: &colab.Connection{URL: proxy.URL, Token: "runtime-token", ExpireTime: time.Now().Add(time.Hour)}}
	assertRemoteLog(t, m, s, "log from the command terminal\n")
}

func assertRemoteLog(t *testing.T, m *Manager, s *Session, want string) {
	t.Helper()
	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	m.Logs(s, func(out string, err error) { ch <- result{out, err} })
	select {
	case got := <-ch:
		if got.err != nil || got.out != want {
			t.Errorf("returned %d bytes with error %v, want %d bytes", len(got.out), got.err, len(want))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("log callback did not finish")
	}
}
