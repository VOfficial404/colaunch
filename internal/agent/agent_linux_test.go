package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Stopping a plugin kills its process only while the pid is still the
// plugin's, not another process's that got the number since.
func TestStopChecksThePID(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	run := filepath.Join(t.TempDir(), "run.sh")
	os.WriteFile(run, []byte("exec sleep 60\n"), 0o644)
	if out, err := agent(t, home, "run", "p", run); err != nil {
		t.Fatal(out, err)
	}
	pidFile := filepath.Join(home, "plugins", "p", "pid")
	b, _ := os.ReadFile(pidFile)
	var rec struct {
		PID   int    `json:"pid"`
		Start *int64 `json:"start"`
	}
	if err := json.Unmarshal(b, &rec); err != nil || rec.PID == 0 || rec.Start == nil {
		t.Fatalf("pid file %s: %v", b, err)
	}

	other := exec.Command("sleep", "60")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Process.Kill()
	// The pid file naming another process, as after the plugin ended and
	// its number was reused: with the plugin's start time, and as an
	// older file's bare number.
	for _, fake := range []string{
		`{"pid": ` + itoa(other.Process.Pid) + `, "start": ` + itoa(int(*rec.Start)) + `}`,
		itoa(other.Process.Pid),
	} {
		os.WriteFile(pidFile, []byte(fake), 0o600)
		agent(t, home, "stop", "p")
		if other.Process.Signal(syscall.Signal(0)) != nil {
			t.Fatalf("stopping with pid file %s killed another process", fake)
		}
	}
	os.WriteFile(pidFile, b, 0o600)
	agent(t, home, "stop", "p")
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(rec.PID, 0) == nil && time.Now().Before(deadline) {
		if st, _ := os.ReadFile("/proc/" + itoa(rec.PID) + "/stat"); strings.Contains(string(st), ") Z ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st, err := os.ReadFile("/proc/" + itoa(rec.PID) + "/stat"); err == nil && !strings.Contains(string(st), ") Z ") {
		t.Errorf("the plugin's process is still running: %s", st)
	}
}
