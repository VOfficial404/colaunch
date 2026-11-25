package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// agent runs agent.py with its home in home.
func agent(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "agent.py")
	os.WriteFile(script, Script, 0o644)
	cmd := exec.Command(python(t), append([]string{script}, args...)...)
	// No proxy of the machine's between the agent and the tests' servers.
	cmd.Env = append(os.Environ(), "COLAUNCH_HOME="+home, "NO_PROXY=*", "no_proxy=*")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Plugins' folders are their owner's alone, each run's secrets in a
// folder of their own, and plugin IDs can't lead out of them.
func TestPrivateFolders(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	d, err := agent(t, home, "prepare", "p1")
	if err != nil {
		t.Fatal(d, err)
	}
	a, err := agent(t, home, "private", "p1")
	if err != nil {
		t.Fatal(a, err)
	}
	b, _ := agent(t, home, "private", "p1")
	if a == b || !strings.HasPrefix(a, filepath.Join(home, "plugins", "p1", "private")) {
		t.Errorf("private folders %q and %q", a, b)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{home, filepath.Join(home, "plugins"), d, a} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("%s: %v %v", p, fi.Mode(), err)
			}
		}
	}
	f := filepath.Join(a, "hf-token")
	os.WriteFile(f, []byte("t"), 0o644)
	if out, err := agent(t, home, "seal", f); err != nil {
		t.Errorf("seal: %s %v", out, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
			t.Errorf("sealed file is %v", fi.Mode())
		}
	}
	outside := filepath.Join(t.TempDir(), "x")
	os.WriteFile(outside, nil, 0o644)
	if _, err := agent(t, home, "seal", outside); err == nil {
		t.Error("sealed a file outside the plugins")
	}
	for _, id := range []string{"../x", "a/b", "", "UP"} {
		if _, err := agent(t, home, "prepare", id); err == nil {
			t.Errorf("prepared %q", id)
		}
	}
	// Stopping removes what's left of the secrets.
	agent(t, home, "stop", "p1")
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("the secrets' folder stayed: %v", err)
	}
}

// noticeServer records the notices it gets, the reason as it came.
func noticeServer(t *testing.T) (*httptest.Server, chan map[string]string) {
	got := make(chan map[string]string, 10)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]string
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("not JSON: %s", b)
		}
		body["path"], body["auth"] = r.URL.Path, r.Header.Get("Authorization")
		got <- body
	}))
	t.Cleanup(ts.Close)
	return ts, got
}

func writeNotices(t *testing.T, home string, n Notices) {
	t.Helper()
	dir, err := agent(t, home, "notices", "prepare")
	if err != nil {
		t.Fatal(dir, err)
	}
	b, _ := json.Marshal(n)
	if err := os.WriteFile(filepath.Join(dir, "notices.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := agent(t, home, "notices", "seal"); err != nil {
		t.Fatal(out, err)
	}
}

// The notices kept for the watchdog are its owner's alone, and go out
// with the reason put in, each in turn: one that fails stops none other.
func TestNotices(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	ts, got := noticeServer(t)
	writeNotices(t, home, Notices{
		Reasons: map[string]string{"idle": `闲置停止 "quoted"`},
		Requests: []Notice{
			{Method: "POST", URL: "http://127.0.0.1:1/dead", Body: `{"x":"{reason}"}`},
			{Method: "POST", URL: "file:///etc/passwd", Body: `{}`},
			{Method: "POST", URL: ts.URL + "/hook", Headers: map[string]string{"Authorization": "Bearer t1", "Content-Type": "application/json"},
				Body: `{"body":"Reason: {reason}","time":"{time}"}`},
		},
	})
	if runtime.GOOS != "windows" {
		for p, mode := range map[string]os.FileMode{filepath.Join(home, "notices"): 0o700, filepath.Join(home, "notices", "notices.json"): 0o600} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != mode {
				t.Errorf("%s: %v %v", p, fi.Mode(), err)
			}
		}
	}
	out, err := agent(t, home, "notify", "idle")
	if err != nil || out != "sent 1" {
		t.Fatalf("notify: %q %v", out, err)
	}
	select {
	case b := <-got:
		if b["body"] != `Reason: 闲置停止 "quoted"` || b["path"] != "/hook" || b["auth"] != "Bearer t1" {
			t.Errorf("got %v", b)
		}
		if _, err := time.Parse(time.RFC3339, b["time"]); err != nil {
			t.Errorf("time %q", b["time"])
		}
	default:
		t.Fatal("no notice")
	}
	// A reason without words goes as itself.
	agent(t, home, "notify", "schedule")
	if b := <-got; b["body"] != "Reason: schedule" {
		t.Errorf("got %v", b)
	}
	if out, err := agent(t, home, "notices", "clear"); err != nil {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(home, "notices", "notices.json")); !os.IsNotExist(err) {
		t.Errorf("clear left the file: %v", err)
	}
	if out, _ := agent(t, home, "notify", "idle"); out != "sent 0" {
		t.Errorf("with none kept: %q", out)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
