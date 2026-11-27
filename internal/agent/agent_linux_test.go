package agent

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func TestMonitorKeepsReadyFreshUntilDeadline(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	dir := filepath.Join(home, "plugins", "p")
	script := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(script, []byte("touch ready\nexec sleep 60\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const nonce = "00000000000000000000000000000001"
	check := base64.StdEncoding.EncodeToString([]byte("test -f " + shellQuote(filepath.Join(dir, "ready"))))
	if out, err := agent(t, home, "run", "p", script, nonce, check, "14"); err != nil {
		t.Fatal(out, err)
	}
	t.Cleanup(func() { agent(t, home, "stop", "p") })
	expiry := time.Now().Add(14 * time.Second)
	path := filepath.Join(dir, "wait-"+nonce+".json")
	var snapshot struct {
		State     string `json:"state"`
		UpdatedAt int64  `json:"updated_at"`
	}
	read := func() {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			snapshot.State = ""
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &snapshot); err != nil {
			t.Fatalf("snapshot %s: %v", b, err)
		}
	}
	var firstReady int64
	for time.Now().Before(expiry) {
		read()
		if snapshot.State == "ready" {
			if firstReady == 0 {
				firstReady = snapshot.UpdatedAt
			} else if snapshot.UpdatedAt > firstReady {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snapshot.State != "ready" || snapshot.UpdatedAt <= firstReady {
		t.Fatalf("ready was never refreshed: first timestamp %d, last snapshot %+v", firstReady, snapshot)
	}
	last := snapshot
	time.Sleep(time.Until(expiry) + 250*time.Millisecond)
	read()
	if snapshot != last {
		t.Fatalf("monitor expiry changed the snapshot: before %+v, after %+v", last, snapshot)
	}
	if out, err := agent(t, home, "running", "p"); err != nil || out != "yes" {
		t.Fatalf("plugin after monitor expiry: %q, %v", out, err)
	}
}

// The watchdog sends its notices once, before it gives the runtime back.
func TestWatchdogNotifiesBeforeUnassign(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	var mu sync.Mutex
	var order []string
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		order = append(order, r.URL.Path+" "+string(b))
		if r.URL.Path == "/unassign" {
			close(done)
		}
	}))
	defer ts.Close()
	t.Setenv("TBE_RUNTIME_ADDR", strings.TrimPrefix(ts.URL, "http://"))
	writeNotices(t, home, Notices{
		Reasons:  map[string]string{"schedule": "on schedule"},
		Requests: []Notice{{Method: "POST", URL: ts.URL + "/hook", Body: `{"r":"{reason}"}`}},
	})
	cfg, _ := json.Marshal(Watchdog{StopAt: time.Now().Add(-time.Minute).Unix()})
	os.WriteFile(filepath.Join(home, "watchdog.json"), cfg, 0o600)
	if out, err := agent(t, home, "watchdog"); err != nil {
		t.Fatal(out, err)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the watchdog didn't unassign")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != `/hook {"r":"on schedule"}` || !strings.HasPrefix(order[1], "/unassign") {
		t.Errorf("requests %q", order)
	}
}
