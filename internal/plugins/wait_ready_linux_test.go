package plugins

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"colaunch/internal/agent"
)

func TestWaitReadyProbe(t *testing.T) {
	python := os.Getenv("COLAUNCH_TEST_PYTHON")
	if python == "" {
		var err error
		if python, err = exec.LookPath("python3"); err != nil {
			t.Skip("no Python: set COLAUNCH_TEST_PYTHON")
		}
	}
	comfy := Comfy{}
	id := comfy.ID()
	var ready atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /system_stats", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	check := comfy.readyCheck(srv.Listener.Addr().(*net.TCPAddr).Port)
	home := t.TempDir()
	dir := filepath.Join(home, "plugins", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(home, "agent.py")
	logPath := filepath.Join(dir, "log.txt")
	pidPath := filepath.Join(dir, "pid")
	const log = "init\nloading\rstill loading\n"
	write(script, string(agent.Script))
	write(logPath, log)

	// Keep a process alive for the whole probe without waiting for a timer.
	child := exec.Command("cat")
	child.Env = append(os.Environ(), "COLAUNCH_PLUGIN_DIR="+dir)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		stdin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		child.Wait()
	})
	pid := strconv.Itoa(child.Process.Pid)
	cmd := exec.Command(python, "-c", `import runpy, sys
runpy.run_path(sys.argv[1])["write_pid"](sys.argv[2], int(sys.argv[3]))`, script, pidPath, pid)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writing PID record: %s %v", out, err)
	}
	current, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Start *int64 `json:"start"`
	}
	if err := json.Unmarshal(current, &rec); err != nil || rec.Start == nil {
		t.Fatalf("PID record %s: %v", current, err)
	}

	// The agent answers requests on its stdin, as on its terminal.
	const readyMarker = "@@COLAUNCH-WAIT-READY-test@@"
	mon := exec.Command(python, "-u", script, "wait-ready", id, strconv.Itoa(waitLogChunk), readyMarker)
	mon.Env = append(os.Environ(), "COLAUNCH_HOME="+home)
	monIn, _ := mon.StdinPipe()
	monOut, _ := mon.StdoutPipe()
	if err := mon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { monIn.Close(); mon.Wait() })
	lines := bufio.NewReader(monOut)
	if line, err := lines.ReadString('\n'); err != nil || strings.TrimSpace(line) != readyMarker {
		t.Fatalf("agent started with %q, %v", line, err)
	}
	n := 0
	probe := func(t *testing.T, off int, ck string) waitAnswer {
		t.Helper()
		n++
		nonce := fmt.Sprintf("nonce%d", n)
		fmt.Fprintf(monIn, "W %s %d %s\n", nonce, off, base64.StdEncoding.EncodeToString([]byte(ck)))
		line, err := lines.ReadString('\n')
		head := "@@WR " + nonce + " "
		if err != nil || !strings.HasPrefix(line, head) {
			t.Fatalf("answer %q, %v", line, err)
		}
		a, ok := parseWaitAnswer(line[len(head):], int64(off))
		if !ok {
			t.Fatalf("bad answer %q", line)
		}
		return a
	}
	running := map[string]bool{"yes": true, "no": false, "unknown": true}
	for _, tc := range []struct{ name, record, want string }{
		{"live JSON", string(current), "yes"},
		{"live legacy", pid + "\n", "yes"},
		{"reused PID", fmt.Sprintf(`{"pid":%s,"start":%d}`, pid, *rec.Start+1), "no"},
		{"unowned legacy", strconv.Itoa(os.Getpid()), "no"},
		{"malformed", "not a PID", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(pidPath, tc.record)
			a := probe(t, 0, check)
			if a.ready || a.size != int64(len(log)) || a.from != 0 || string(a.data) != log || a.running != running[tc.want] {
				t.Errorf("probe = %+v, want %s", a, tc.want)
			}
		})
	}
	ready.Store(true)
	if a := probe(t, 0, check); !a.ready {
		t.Errorf("ready probe = %+v", a)
	}
	ready.Store(false)

	// The last error, written just before liveness is checked, is read.
	write(pidPath, strconv.Itoa(os.Getpid()))
	const finalError = "fatal: couldn't clone ComfyUI\n"
	appendErr := "printf '%s' " + ShellQuote(finalError) + " >> " + ShellQuote(logPath) + "; false"
	if a := probe(t, 0, appendErr); a.running || string(a.data) != log+finalError {
		t.Errorf("final probe = %+v", a)
	}

	const maxBytes = 16 << 10
	const largeOffset = 6_000_000
	backlog := strings.Repeat("x", maxBytes) + strings.Repeat("y", largeOffset-maxBytes) + strings.Repeat("z", maxBytes) + log + finalError
	write(logPath, backlog)
	for _, tc := range []struct {
		name, record string
		running      bool
		off, from    int
	}{
		{"first chunk", string(current), true, 0, 0},
		{"next chunk", string(current), true, maxBytes, maxBytes},
		{"large offset", string(current), true, largeOffset, largeOffset},
		{"log shrank", string(current), true, len(backlog) + 1, 0},
		{"stopped tail", strconv.Itoa(os.Getpid()), false, 0, len(backlog) - maxBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(pidPath, tc.record)
			a := probe(t, tc.off, check)
			if a.from != int64(tc.from) || a.running != tc.running || string(a.data) != backlog[tc.from:tc.from+maxBytes] {
				t.Errorf("probe has %d bytes from %d, running %t; want [%d, %d)", len(a.data), a.from, a.running, tc.from, tc.from+maxBytes)
			}
		})
	}
}
