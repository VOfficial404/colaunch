package plugins

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	const id = "p"
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

	probe := func(check, beforeRunning string) string {
		t.Helper()
		command := strings.NewReplacer(
			"python3 "+ShellQuote(agent.Path), beforeRunning+ShellQuote(python)+" "+ShellQuote(script),
			ShellQuote(agent.PluginDir(id)+"/log.txt"), ShellQuote(logPath),
		).Replace(waitReadyProbe(id, 0, check))
		cmd := exec.Command("bash", "-c", command)
		cmd.Env = append(os.Environ(), "COLAUNCH_HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("probe: %s %v", out, err)
		}
		return string(out)
	}
	for _, tc := range []struct{ name, record, want string }{
		{"live JSON", string(current), "yes"},
		{"live legacy", pid + "\n", "yes"},
		{"reused PID", fmt.Sprintf(`{"pid":%s,"start":%d}`, pid, *rec.Start+1), "no"},
		{"unowned legacy", strconv.Itoa(os.Getpid()), "no"},
		{"malformed", "not a PID", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(pidPath, tc.record)
			want := fmt.Sprintf("@@WAIT %d 0 %s@@\n%s\n", len(log), tc.want, base64.StdEncoding.EncodeToString([]byte(log)))
			if got := probe("false", ""); got != want {
				t.Errorf("probe = %q, want %q", got, want)
			}
		})
	}
	if got := probe("true", ""); got != "@@WAIT-READY@@\n" {
		t.Errorf("ready probe = %q", got)
	}

	// Simulate the last error arriving during the liveness check.
	write(pidPath, strconv.Itoa(os.Getpid()))
	const finalError = "fatal: couldn't clone ComfyUI\n"
	prefix := "printf '%s' " + ShellQuote(finalError) + " >> " + ShellQuote(logPath) + "; "
	want := fmt.Sprintf("@@WAIT %d 0 no@@\n%s\n", len(log+finalError), base64.StdEncoding.EncodeToString([]byte(log+finalError)))
	if got := probe("false", prefix); got != want {
		t.Errorf("final probe = %q, want %q", got, want)
	}
}
