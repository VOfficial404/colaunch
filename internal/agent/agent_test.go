package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	cmd.Env = append(os.Environ(), "COLAUNCH_HOME="+home)
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

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
