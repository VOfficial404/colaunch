package plugins

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"colaunch/internal/colab"
)

type probeReply struct {
	command string
	output  string
	noStart bool
	noEnd   bool
}

// Use a real Shell and /colab/tty WebSocket, scripting the terminal's frames
// so that dropping START is deterministic even without a runtime or a PTY.
func probeShell(t *testing.T, replies ...probeReply) *colab.Shell {
	t.Helper()
	replies = append([]probeReply{{command: "true"}}, replies...)
	frame := regexp.MustCompile(`^echo @@S""TART-([0-9]+)@@; \(eval "\$\(echo ([A-Za-z0-9+/=]+) \| base64 -d\)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		for n := 0; ; {
			var msg struct{ Data string }
			if err := wsjson.Read(r.Context(), ws, &msg); err != nil {
				return
			}
			if msg.Data == "" || strings.HasPrefix(msg.Data, "exec env ") || strings.HasPrefix(msg.Data, "stty -echo ") {
				continue
			}
			m := frame.FindStringSubmatch(msg.Data)
			if n >= len(replies) || m == nil {
				t.Errorf("unexpected command %q", msg.Data)
				return
			}
			reply := replies[n]
			n++
			command, err := base64.StdEncoding.DecodeString(m[2])
			if err != nil || string(command) != reply.command {
				t.Errorf("command %d = %q, want %q (decode: %v)", n, command, reply.command, err)
				return
			}
			start := fmt.Sprintf("@@START-%s@@\n", m[1])
			if reply.noStart {
				start = strings.TrimSuffix(start, "@\n")
			}
			end := fmt.Sprintf("@@END-%s:0@@\n", m[1])
			if reply.noEnd {
				end = ""
			}
			if err := wsjson.Write(r.Context(), ws, map[string]string{"data": start + reply.output + end}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sh, err := colab.OpenShell(ctx, &colab.Connection{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sh.Close() })
	return sh
}

func probeLog(size, from int, data, running string) string {
	return fmt.Sprintf("@@WAIT %d %d %s@@\n%s\n", size, from, running, base64.StdEncoding.EncodeToString([]byte(data)))
}

func TestWaitReadyRecoversMissingStart(t *testing.T) {
	const id, check = "comfyui", "curl -sf -o /dev/null http://127.0.0.1:8188/system_stats"
	const phase = "@@PHASE installing@@\n"
	const progress = "10%\r"
	first := phase + strings.Repeat("\n", waitLogChunk-len(phase)-len(progress)) + progress
	second := "100%\n" + strings.Repeat("\n", waitLogChunk-len("100%\n"))
	const size = 2*waitLogChunk + 1
	probe := func(off int64) string { return waitReadyProbe(id, off, check) }
	sh := probeShell(t,
		probeReply{command: probe(0), noStart: true},
		probeReply{command: probe(0), noStart: true},
		probeReply{command: probe(0), output: probeLog(size, 0, first, "yes")},
		probeReply{command: probe(waitLogChunk), output: probeLog(size, waitLogChunk, second, "yes"), noStart: true},
		probeReply{command: probe(waitLogChunk), output: probeLog(size, waitLogChunk, second, "yes")},
		probeReply{command: probe(2 * waitLogChunk), output: "@@WAIT-READY@@\n"},
	)
	var phases, logs []string
	env := &Env{Shell: sh, Phase: func(p string) { phases = append(phases, p) }, Log: func(f string, args ...any) {
		logs = append(logs, fmt.Sprintf(f, args...))
	}}
	if err := waitReady(t.Context(), env, id, 5*time.Second, check); err != nil {
		t.Fatal(err)
	}
	if strings.Join(phases, " ") != "installing" || strings.Join(logs, " ") != "100%" {
		t.Fatalf("phases %q, logs %q", phases, logs)
	}
}

func TestWaitReadyBoundsMissingStarts(t *testing.T) {
	const id, check = "p", "false"
	missing := probeReply{command: waitReadyProbe(id, 0, check), noStart: true}
	sh := probeShell(t, probeReply{command: "echo once", noStart: true}, missing, missing, missing)
	if _, _, err := sh.Run(t.Context(), "echo once"); !errors.Is(err, colab.ErrMissingStart) || err.Error() != "colab: the output of command 2 has no start" {
		t.Fatalf("Run error = %v", err)
	}
	if err := waitReady(t.Context(), &Env{Shell: sh}, id, 5*time.Second, check); !errors.Is(err, colab.ErrMissingStart) || err.Error() != "colab: the output of command 5 has no start" {
		t.Fatalf("waitReady error = %v", err)
	}
}

func TestWaitReadyTimesOutWithoutEnd(t *testing.T) {
	const id, check = "p", "false"
	const timeout = 200 * time.Millisecond
	missing := probeReply{command: waitReadyProbe(id, 0, check), noStart: true}
	sh := probeShell(t, missing, probeReply{command: missing.command, noStart: true, noEnd: true})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := waitReady(ctx, &Env{Shell: sh}, id, timeout, check); err == nil || err.Error() != "the plugin didn't get ready in 200ms" {
		t.Fatalf("waitReady error = %v", err)
	}
}

func TestWaitReadyKeepsStoppedTail(t *testing.T) {
	const id, check = "p", "false"
	const last = "\n20%\rfatal: dependency install failed"
	tail := strings.Repeat("\n", waitLogChunk-len(last)) + last
	probe := waitReadyProbe(id, 0, check)
	for _, tc := range []struct {
		name    string
		replies []probeReply
	}{
		{"snapshot", []probeReply{{command: probe, output: probeLog(3*waitLogChunk, 2*waitLogChunk, tail, "no")}}},
		{"fallback", []probeReply{
			{command: probe, output: probeLog(3*waitLogChunk, 0, strings.Repeat("\n", waitLogChunk), "no")},
			{command: "python3 '/content/colaunch/agent.py' logs 'p' 50 | tail -c 16384", output: "fatal: dependency install failed"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := &Env{Shell: probeShell(t, tc.replies...), Log: func(string, ...any) {}}
			if err := waitReady(t.Context(), env, id, 5*time.Second, check); err == nil || err.Error() != "the plugin stopped: fatal: dependency install failed" {
				t.Fatalf("waitReady error = %v", err)
			}
		})
	}
}
