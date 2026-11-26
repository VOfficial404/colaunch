package plugins

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/e2e"
)

// The real agent and bash, on terminals that break as Colab's did: the
// first garbles two answers, the next prints an earlier terminal's output
// before its own and drops after one answer. The wait reads on through a
// third, while the one installer, started once, runs on and gets ready.
// Each line of its log shows once, in order.
func TestWaitReadyThroughFaultyTerminals(t *testing.T) {
	const id = "comfyui"
	home := filepath.ToSlash(t.TempDir())
	dir := home + "/plugins/" + id
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := home + "/agent.py"
	if err := os.WriteFile(script, agent.Script, 0o600); err != nil {
		t.Fatal(err)
	}
	install := home + "/install.sh"
	os.WriteFile(install, []byte(`echo start
for i in $(seq 1 1000); do echo "line $i $(printf '%060d' 0)"; done
echo "@@PHASE installing@@"
while [ ! -f "$1/go" ]; do sleep 0.1; done
touch "$1/ready"
while [ ! -f "$1/stop" ]; do sleep 0.1; done
`), 0o600)

	var opened atomic.Int32
	conn := e2e.Colab(t, func(n int) e2e.Faults {
		opened.Store(int32(n))
		answers := 0
		answer := func(line string) bool {
			if strings.Contains(line, "@@WR ") {
				answers++
				return true
			}
			return false
		}
		switch n {
		case 1:
			return e2e.Faults{Line: func(line string) (string, bool) {
				if answer(line) && answers > 1 {
					return line[:len(line)/2], true
				}
				return line, true
			}}
		case 2:
			return e2e.Faults{
				Stale: "@@END-1:0@@\r\n@@WR 000000000000 b0c0ffee ready@@\r\n",
				Line: func(line string) (string, bool) {
					return line, !answer(line) || answers < 2
				},
			}
		}
		return e2e.Faults{}
	})

	installer := exec.Command(e2e.Bash(t), install, home)
	logFile, err := os.Create(dir + "/log.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	installer.Stdout, installer.Stderr = logFile, logFile
	if err := installer.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { installer.Wait(); close(exited) }()
	t.Cleanup(func() {
		os.WriteFile(home+"/stop", nil, 0o600)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			installer.Process.Kill()
		}
	})

	oldTimeout, oldPoll := waitProbeTimeout, waitPollInterval
	waitProbeTimeout, waitPollInterval = 20*time.Second, 100*time.Millisecond
	t.Cleanup(func() { waitProbeTimeout, waitPollInterval = oldTimeout, oldPoll })
	var logs, phases []string
	env := &Env{
		Log: func(f string, args ...any) {
			logs = append(logs, fmt.Sprintf(f, args...))
			// Ready once the whole log is read.
			if strings.HasPrefix(logs[len(logs)-1], "line 1000 ") {
				os.WriteFile(home+"/go", nil, 0o600)
			}
		},
		Phase: func(p string) { phases = append(phases, p) },
		Stream: func(ctx context.Context, command, ready string) (StreamConn, *bufio.Reader, error) {
			command = strings.Replace(command, "python3 -u "+agent.Path,
				"env COLAUNCH_HOME="+ShellQuote(home)+" python3 -u "+ShellQuote(script), 1)
			tty, r, err := colab.Stream(ctx, conn, command, ready)
			if err != nil {
				return nil, nil, err
			}
			return tty, r, nil
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := waitReady(ctx, env, id, 90*time.Minute, "test -f "+ShellQuote(home+"/ready")); err != nil {
		t.Fatalf("%v; logs %q", err, lastLines(strings.Join(logs, "\n"), 5))
	}
	want := []string{"start"}
	for i := 1; i <= 1000; i++ {
		want = append(want, fmt.Sprintf("line %d %060d", i, 0))
	}
	var shown, notes []string
	for _, l := range logs {
		if strings.HasPrefix(l, "The terminal stopped answering") {
			notes = append(notes, l)
		} else {
			shown = append(shown, l)
		}
	}
	if strings.Join(shown, "\n") != strings.Join(want, "\n") {
		t.Errorf("log shown has %d lines, want %d: %q…", len(shown), len(want), lastLines(strings.Join(shown, "\n"), 3))
	}
	if opened.Load() != 3 || len(notes) != 2 || strings.Join(phases, " ") != "installing" {
		t.Errorf("%d terminals, notes %q, phases %q", opened.Load(), notes, phases)
	}
	select {
	case <-exited:
		t.Error("the installer ended")
	default:
	}
}
