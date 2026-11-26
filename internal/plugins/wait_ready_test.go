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

const testProbeToken = "0123456789ab"

const readyProbeOutput = "@@WAIT-READY " + testProbeToken + "@@\n"

type probeReply struct {
	command string
	output  string
	status  int
	noStart bool
	noEnd   bool
}

// Use a real Shell and /colab/tty WebSocket, scripting the terminal's frames
// so that dropping START is deterministic even without a runtime or a PTY.
func probeShell(t *testing.T, replies ...probeReply) *colab.Shell {
	t.Helper()
	old := newProbeToken
	newProbeToken = func() string { return testProbeToken }
	t.Cleanup(func() { newProbeToken = old })
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
			if strings.Contains(msg.Data, "stty raw") {
				t.Errorf("command %d attempted a script upload", n)
				return
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
			end := fmt.Sprintf("@@END-%s:%d@@\n", m[1], reply.status)
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
	return fmt.Sprintf("@@WAIT %s %d %d %s@@\n%s\n", testProbeToken, size, from, running, base64.StdEncoding.EncodeToString([]byte(data)))
}

func TestWaitReadyBuiltinProbesStayInline(t *testing.T) {
	for _, tc := range []struct{ id, check string }{
		{Comfy{}.ID(), Comfy{}.readyCheck(ComfyPort)},
		{Llama{}.ID(), Llama{}.readyCheck()},
	} {
		t.Run(tc.id, func(t *testing.T) {
			const off = 123_456_789
			const data = "loading\n"
			command := waitReadyProbe(tc.id, testProbeToken, off, tc.check)
			encoded := base64.StdEncoding.EncodedLen(len(command))
			t.Logf("offset %d: %d command bytes, %d base64 bytes", off, len(command), encoded)
			if encoded > 700 {
				t.Errorf("encoded command exceeds the 700-byte tty.Run limit")
			}
			sh := probeShell(t, probeReply{command: command, output: probeLog(off+len(data), off, data, "yes")})
			a, _, err := runWaitProbe(t.Context(), sh, tc.id, off, tc.check)
			if err != nil || a.from != off || string(a.data) != data || !a.running {
				t.Fatalf("probe = %+v, error = %v", a, err)
			}
		})
	}
}

func TestWaitReadyRecoversMissingStart(t *testing.T) {
	id, check := Comfy{}.ID(), Comfy{}.readyCheck(ComfyPort)
	const phase = "@@PHASE installing@@\n"
	const progress = "10%\r"
	first := phase + strings.Repeat("\n", waitLogChunk-len(phase)-len(progress)) + progress
	second := "100%\n" + strings.Repeat("\n", waitLogChunk-len("100%\n"))
	const size = 2*waitLogChunk + 1
	probe := func(off int64) string { return waitReadyProbe(id, testProbeToken, off, check) }
	sh := probeShell(t,
		probeReply{command: probe(0), noStart: true},
		probeReply{command: probe(0), noStart: true},
		probeReply{command: probe(0), output: probeLog(size, 0, first, "yes")},
		probeReply{command: probe(waitLogChunk), output: probeLog(size, waitLogChunk, second, "yes"), noStart: true},
		probeReply{command: probe(2 * waitLogChunk), output: readyProbeOutput},
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
	missing := probeReply{command: waitReadyProbe(id, testProbeToken, 0, check), noStart: true}
	sh := probeShell(t, probeReply{command: "echo once", noStart: true}, missing, missing, missing)
	if _, _, err := sh.Run(t.Context(), "echo once"); !errors.Is(err, colab.ErrMissingStart) || err.Error() != "colab: the output of command 2 has no start" {
		t.Fatalf("Run error = %v", err)
	}
	if err := waitReady(t.Context(), &Env{Shell: sh}, id, 5*time.Second, check); !errors.Is(err, colab.ErrMissingStart) || err.Error() != "colab: the output of command 5 has no start" {
		t.Fatalf("waitReady error = %v", err)
	}
}

func TestWaitReadyRejectsFailedProbe(t *testing.T) {
	const id, check = "p", "false"
	probe := waitReadyProbe(id, testProbeToken, 0, check)
	for _, noStart := range []bool{false, true} {
		failed := probeReply{command: probe, output: readyProbeOutput, status: 1, noStart: noStart}
		sh := probeShell(t, failed, failed, failed)
		if err := waitReady(t.Context(), &Env{Shell: sh}, id, 5*time.Second, check); err == nil || !strings.Contains(err.Error(), "exited with status 1") {
			t.Fatalf("waitReady error with noStart=%t: %v", noStart, err)
		}
	}
}

func TestWaitReadyReconnectsAfterMalformedReplies(t *testing.T) {
	comfy := Comfy{}
	id, check := comfy.ID(), comfy.readyCheck(ComfyPort)
	probe := func(off int64) string { return waitReadyProbe(id, testProbeToken, off, check) }
	first := strings.Repeat("\n", waitLogChunk-len("installing")) + "installing"
	for _, tc := range []struct {
		name string
		bad  probeReply
	}{
		{"missing START", probeReply{command: probe(waitLogChunk), noStart: true}},
		{"truncated log", probeReply{command: probe(waitLogChunk), output: probeLog(waitLogChunk+10, waitLogChunk, "truncated", "yes")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh := probeShell(t, probeReply{command: probe(0), output: probeLog(waitLogChunk+3, 0, first, "yes")}, tc.bad, tc.bad)
			next := probeShell(t, probeReply{command: probe(waitLogChunk), output: readyProbeOutput})
			env := &Env{Shell: sh, Reconnect: func(ctx context.Context) (*colab.Shell, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > waitReconnectTimeout {
					t.Errorf("reconnect deadline = %v", deadline)
				}
				sh.Close()
				return next, nil
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if err := waitReady(ctx, env, id, 90*time.Minute, check); err != nil {
				t.Fatal(err)
			}
			if env.Shell != next {
				t.Fatal("waitReady kept the failed terminal")
			}
		})
	}
}

func TestWaitReadyTimesOutWithoutEnd(t *testing.T) {
	const id, check = "p", "false"
	const timeout = 200 * time.Millisecond
	missing := probeReply{command: waitReadyProbe(id, testProbeToken, 0, check), noStart: true}
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
	probe := waitReadyProbe(id, testProbeToken, 0, check)
	sh := probeShell(t,
		probeReply{command: probe, output: probeLog(3*waitLogChunk, 0, strings.Repeat("\n", waitLogChunk), "no")},
		probeReply{command: probe, output: probeLog(3*waitLogChunk, 2*waitLogChunk, tail, "no")},
	)
	env := &Env{Shell: sh, Log: func(string, ...any) {}}
	if err := waitReady(t.Context(), env, id, 5*time.Second, check); err == nil || err.Error() != "the plugin stopped: fatal: dependency install failed" {
		t.Fatalf("waitReady error = %v", err)
	}
}
