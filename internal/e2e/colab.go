// Package e2e runs Colaunch's side of a runtime against a stand-in for
// Colab's terminal: a WebSocket that speaks /colab/tty's JSON to a bash in
// a pseudo-terminal, as Colab's does. Where Python has no pty module, as
// on Windows, the bash reads a pipe instead. Other packages' tests use the
// stand-in too.
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"colaunch/internal/colab"
)

// Python is the Python the runtime's side runs with; without one, the
// test is skipped.
func Python(t testing.TB) string {
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

// Bash is the runtime's bash: on Windows Git's, not System32's, which
// starts WSL; without one, the test is skipped.
func Bash(t testing.TB) string {
	if runtime.GOOS != "windows" {
		return "bash"
	}
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

// Faults are what one terminal of the stand-in does wrong.
type Faults struct {
	// Stale is printed as soon as the terminal opens, before anything its
	// bash prints, as output of an earlier terminal replayed.
	Stale string
	// Line, if set, sees each whole line the terminal prints, without its
	// newline, and returns what to print instead, or false to drop the
	// connection there.
	Line func(line string) (string, bool)
}

// Colab serves /colab/tty as Colab's proxy does, behind its token. faults,
// if set, is called with each terminal's number, from 1, as it opens.
func Colab(t testing.TB, faults func(n int) Faults) *colab.Connection {
	py := Python(t)
	// The shim below is named python3: it must not find itself.
	if abs, err := exec.LookPath(py); err == nil {
		py = abs
	}
	bash := Bash(t)
	bin := t.TempDir()
	// The terminal's python3 is the test's.
	os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\nexec \""+filepath.ToSlash(py)+"\" \"$@\"\n"), 0o755)
	path := bin
	if runtime.GOOS == "windows" {
		// Python finds this bash, not System32's, for what it runs.
		path += string(os.PathListSeparator) + filepath.Dir(bash)
	}
	path += string(os.PathListSeparator) + os.Getenv("PATH")
	var opened atomic.Int32
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
		var f Faults
		if faults != nil {
			f = faults(int(opened.Add(1)))
		}
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command(bash)
		} else {
			cmd = exec.Command(py, "-c", "import pty; pty.spawn(['bash', '--norc', '-i'])")
		}
		cmd.Env = append(os.Environ(), "PATH="+path, "TERM=xterm")
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			t.Error(err)
			return
		}
		defer cmd.Process.Kill()
		send := func(s string) error {
			if os.Getenv("E2E_DEBUG") != "" {
				t.Logf("tty> %q", s)
			}
			msg, _ := json.Marshal(map[string]string{"data": s})
			return ws.Write(context.Background(), websocket.MessageText, msg)
		}
		if f.Stale != "" {
			send(f.Stale)
		}
		go func() {
			b := make([]byte, 32<<10)
			var pending string
			for {
				n, err := stdout.Read(b)
				out := string(b[:n])
				if f.Line != nil {
					pending += out
					out = ""
					for {
						line, rest, ok := strings.Cut(pending, "\n")
						if !ok {
							break
						}
						pending = rest
						line, keep := f.Line(line)
						if !keep {
							ws.Close(websocket.StatusGoingAway, "")
							return
						}
						out += line + "\n"
					}
				}
				if out != "" {
					send(out)
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
