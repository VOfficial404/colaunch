package colab

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const comfyLogPath = "colaunch/plugins/comfyui/log.txt"

func TestPluginLogContents(t *testing.T) {
	const token = "test-runtime-proxy-token"
	for _, tc := range []struct {
		name, path, kind, format, content, want, wantErr string
		status                                           int
	}{
		{name: "text", path: comfyLogPath, kind: "file", format: "text", content: "ComfyUI ready\n" + token, want: "ComfyUI ready\n[redacted]"},
		{name: "base64", path: "/" + comfyLogPath, kind: "file", format: "base64", content: base64.StdEncoding.EncodeToString([]byte("ComfyUI starting\n")), want: "ComfyUI starting\n"},
		{name: "wrong file", path: "colaunch/plugins/other/log.txt", kind: "file", format: "text", content: token, wantErr: "requested log file"},
		{name: "directory", path: comfyLogPath, kind: "directory", format: "text", wantErr: "requested log file"},
		{name: "unknown format", path: comfyLogPath, kind: "file", format: "json", wantErr: "unsupported log file format"},
		{name: "invalid base64", path: comfyLogPath, kind: "file", format: "base64", content: token, wantErr: "invalid base64"},
		{name: "404", status: http.StatusNotFound, wantErr: "relative log file unavailable (HTTP 404)"},
		{name: "403", status: http.StatusForbidden, wantErr: "HTTP 403"},
		{name: "oversized", path: comfyLogPath, kind: "file", format: "text", content: strings.Repeat("a", maxContentsLog), wantErr: "size limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/runtime/r1/api/contents/"+comfyLogPath {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
				}
				if r.Header.Get(proxyTokenHeader) != token || r.Header.Get(clientAgentHeader) != ClientAgent || r.Header.Get("Authorization") != "" {
					t.Error("incorrect runtime proxy headers")
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					io.WriteString(w, token)
					return
				}
				json.NewEncoder(w).Encode(map[string]string{"path": tc.path, "type": tc.kind, "format": tc.format, "content": tc.content})
			}))
			defer srv.Close()
			client := &Client{HTTP: srv.Client()}
			out, err := client.PluginLog(t.Context(), &Connection{URL: srv.URL + "/runtime/r1/", Token: token}, "comfyui")
			if tc.wantErr == "" {
				if err != nil || out != tc.want {
					t.Errorf("got %q, %v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), token) || out != "" {
				t.Error("expected a sanitized Contents error and no log output")
			}
			if calls.Load() != 1 {
				t.Errorf("sent %d requests", calls.Load())
			}
		})
	}
}

func TestPluginLogRefusesRedirectAndUnsafeRequests(t *testing.T) {
	const token = "test-proxy-credential"
	var redirected atomic.Int32
	dest := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer dest.Close()
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", dest.URL+"/other/"+token)
		w.WriteHeader(http.StatusTemporaryRedirect)
		io.WriteString(w, token)
	}))
	defer srv.Close()
	client := &Client{HTTP: srv.Client()}
	conn := &Connection{URL: srv.URL, Token: token}
	for _, plugin := range []string{"", "..", "comfyui/../other", "comfyui%2fother", "comfyui\\other", "comfyui?x", "comfyui\n"} {
		if out, err := client.PluginLog(t.Context(), conn, plugin); err == nil || out != "" {
			t.Errorf("invalid plugin ID %q was accepted", plugin)
		}
	}
	for _, proxyURL := range []string{strings.Replace(srv.URL, "https:", "http:", 1), srv.URL + "?leak=" + token} {
		if out, err := client.PluginLog(t.Context(), &Connection{URL: proxyURL, Token: token}, "comfyui"); err == nil || strings.Contains(err.Error(), token) || out != "" {
			t.Error("expected a sanitized error for an unsafe URL")
		}
	}
	if out, err := client.PluginLog(t.Context(), conn, "comfyui"); err == nil || !strings.Contains(err.Error(), "HTTP 307") || strings.Contains(err.Error(), token) || out != "" {
		t.Error("expected a sanitized redirect error")
	}
	if calls.Load() != 1 || redirected.Load() != 0 {
		t.Errorf("sent %d initial and %d redirected requests", calls.Load(), redirected.Load())
	}
}
