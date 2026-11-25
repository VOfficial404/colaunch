// Package e2e runs Colaunch's side of a runtime against a stand-in for
// Colab's terminal: a WebSocket that speaks /colab/tty's JSON to a bash in
// a pseudo-terminal, as Colab's does. Where Python has no pty module, as
// on Windows, the bash reads a pipe instead.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/tunnel"
)

func python(t *testing.T) string {
	if p := os.Getenv("COLAUNCH_TEST_PYTHON"); p != "" {
		return p
	}
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("python3"); err == nil {
			return p
		}
	}
	t.Skip("no Python: set COLAUNCH_TEST_PYTHON")
	return ""
}

// gitBash finds Git's bash on Windows, not System32's, which starts WSL.
func gitBash(t *testing.T) string {
	if p := os.Getenv("COLAUNCH_TEST_BASH"); p != "" {
		return p
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no Git bash")
	}
	for _, rel := range []string{"../bin/bash.exe", "../../bin/bash.exe", "../usr/bin/bash.exe"} {
		p := filepath.Join(filepath.Dir(git), rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no Git bash")
	return ""
}

// fakeColab serves /colab/tty as Colab's proxy does, behind its token.
func fakeColab(t *testing.T) *colab.Connection {
	py := python(t)
	// The shim below is named python3: it must not find itself.
	if abs, err := exec.LookPath(py); err == nil {
		py = abs
	}
	bin := t.TempDir()
	// The terminal's python3 is the test's.
	os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\nexec \""+filepath.ToSlash(py)+"\" \"$@\"\n"), 0o755)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/colab/tty" || r.Header.Get("X-Colab-Runtime-Proxy-Token") != "secret" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command(gitBash(t))
		} else {
			cmd = exec.Command(py, "-c", "import pty; pty.spawn(['bash', '--norc', '-i'])")
		}
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TERM=xterm")
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = cmd.Stdout
		if os.Getenv("E2E_DEBUG") != "" {
			t.Logf("starting %v", cmd.Args)
		}
		if err := cmd.Start(); err != nil {
			t.Error(err)
			return
		}
		defer cmd.Process.Kill()
		go func() {
			b := make([]byte, 32<<10)
			for {
				n, err := stdout.Read(b)
				if os.Getenv("E2E_DEBUG") != "" {
					t.Logf("tty> %q %v", b[:n], err)
				}
				if n > 0 {
					msg, _ := json.Marshal(map[string]string{"data": string(b[:n])})
					ws.Write(context.Background(), websocket.MessageText, msg)
				}
				if err != nil {
					ws.Close(websocket.StatusNormalClosure, "")
					return
				}
			}
		}()
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var m struct {
				Data *string `json:"data"`
			}
			if json.Unmarshal(data, &m) == nil && m.Data != nil {
				io.WriteString(stdin, *m.Data)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return &colab.Connection{URL: srv.URL, Token: "secret", ExpireTime: time.Now().Add(time.Hour)}
}

// Commands run on the terminal give back what they printed and their
// status, apart from the terminal's echo and prompts.
func TestShellRun(t *testing.T) {
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	out, code, err := sh.Run(ctx, "echo hello; echo 'it''s' \"$((6*7))\" >&2; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 || strings.TrimSpace(out) != "hello\nits 42" {
		t.Errorf("got %q, status %d", out, code)
	}
	// A command longer than a terminal's line goes up as a file.
	long := "x=" + strings.Repeat("a", 5000) + "; echo ${#x}"
	out, code, err = sh.Run(ctx, long)
	if err != nil || code != 0 || strings.TrimSpace(out) != "5000" {
		t.Errorf("long command: %q, %d, %v", out, code, err)
	}
	// Commands keep their order and don't read each other's output.
	for i := range 5 {
		out, _, err := sh.Run(ctx, fmt.Sprintf("echo %d", i))
		if err != nil || strings.TrimSpace(out) != fmt.Sprint(i) {
			t.Errorf("command %d: %q %v", i, out, err)
		}
	}
}

// Files go up byte for byte, binary ones too.
func TestShellUpload(t *testing.T) {
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	data := make([]byte, 150<<10)
	rand.Read(data)
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "sub dir", "blob.bin"))
	if err := sh.Upload(ctx, path, data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	out, _, err := sh.Run(ctx, "sha256sum '"+path+"'")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), hex.EncodeToString(sum[:])) {
		t.Errorf("uploaded file's sum %q, want %x", out, sum)
	}
	// The terminal reads commands as before after a file.
	if out, _, _ := sh.Run(ctx, "echo still here"); strings.TrimSpace(out) != "still here" {
		t.Errorf("after the upload: %q", out)
	}
}

// An upload whose folder can't be made, or whose file can't be moved in
// place, fails, leaves nothing behind, and the terminal reads commands on.
func TestShellUploadFails(t *testing.T) {
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("x"), 0o644)
	if err := sh.Upload(ctx, filepath.ToSlash(file)+"/sub/blob", []byte("data")); err == nil {
		t.Error("uploading under a file worked")
	}
	// mv can't put a file in place of a folder.
	os.MkdirAll(filepath.Join(dir, "folder", "inside"), 0o755)
	if err := sh.Upload(ctx, filepath.ToSlash(filepath.Join(dir, "folder")), []byte("data")); err == nil {
		t.Error("a failed mv went unnoticed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("left behind: %v", entries)
	}
	if out, _, _ := sh.Run(ctx, "echo still here"); strings.TrimSpace(out) != "still here" {
		t.Errorf("after the failed uploads: %q", out)
	}
}

// A port of the runtime reaches 127.0.0.1 here through the agent's mux on
// a terminal of its own, the way the app forwards llama-server's.
func TestTunnelOverTerminal(t *testing.T) {
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "agent.py"))
	if err := sh.Upload(ctx, path, agent.Script); err != nil {
		t.Fatal(err)
	}
	// The runtime's service: an echo server.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	tty, r, err := colab.Stream(ctx, conn, "python3 -u '"+path+"' mux", agent.MuxReady)
	if err != nil {
		t.Fatal(err)
	}
	m := tunnel.New(tty, r)
	defer m.Close()
	f, err := tunnel.Listen("127.0.0.1:0", ln.Addr().(*net.TCPAddr).Port, func() (*tunnel.Mux, error) { return m, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := net.Dial("tcp", f.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := make([]byte, 300<<10)
	rand.Read(want)
	go c.Write(want)
	got := make([]byte, len(want))
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("bytes changed through the terminal")
	}
}
