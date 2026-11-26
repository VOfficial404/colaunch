package plugins

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
)

// request is what the agent was asked on a terminal.
type request struct {
	conn  int
	nonce string
	off   int64
	check string
}

// reply is what a scripted terminal prints for a request: raw bytes, the
// connection dropped, or nothing.
type reply struct {
	out  func(nonce string) string
	drop bool
}

func answerLine(nonce, payload string) string {
	return fmt.Sprintf("@@WR %s %08x %s@@\n", nonce, crc32.ChecksumIEEE([]byte(payload)), payload)
}

func readyReply() reply {
	return reply{out: func(n string) string { return answerLine(n, "ready") }}
}

func logReply(size, from int, data, running string) reply {
	return reply{out: func(n string) string {
		return answerLine(n, fmt.Sprintf("log %d %d %s %s", size, from, running, base64.StdEncoding.EncodeToString([]byte(data))))
	}}
}

// badReply is the observed "invalid readiness probe response": the
// answer's line, its nonce right, cut short.
func badReply(size, from int, data string) reply {
	return reply{out: func(n string) string {
		line := logReply(size, from, data, "yes").out(n)
		return line[:len(line)/2] + "\n"
	}}
}

// script is a terminal's part: what it printed before (a replay of an
// earlier terminal's output), and its replies in order.
type script struct {
	stale   string
	replies []reply
}

// monitorEnv serves /colab/tty as `agent.py wait-ready` on it would, one
// script a connection, and returns an Env whose Stream dials it through
// colab.Stream, as the app does, and the requests the terminals read.
func monitorEnv(t *testing.T, id string, scripts ...script) (*Env, func() []request, *atomic.Int32) {
	t.Helper()
	var mu sync.Mutex
	var reqs []request
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		n := int(conns.Add(1))
		if n > len(scripts) {
			t.Errorf("terminal %d opened, %d expected", n, len(scripts))
			return
		}
		sc := scripts[n-1]
		write := func(s string) bool { return wsjson.Write(r.Context(), ws, map[string]string{"data": s}) == nil }
		var in strings.Builder
		started := false
		for {
			var msg struct{ Data *string }
			if err := wsjson.Read(r.Context(), ws, &msg); err != nil {
				return
			}
			if msg.Data == nil {
				continue
			}
			if !started {
				prefix := fmt.Sprintf("stty raw -echo; exec python3 -u %s wait-ready %s %d '", agent.Path, ShellQuote(id), waitLogChunk)
				marker, found := strings.CutPrefix(*msg.Data, prefix)
				if found {
					started = true
					marker = strings.TrimSuffix(marker, "'\n")
					if !write(sc.stale + "\r\n@@COLAUNCH-WAIT-READY-STALE@@\r\n" + marker + "\r\n") {
						return
					}
				}
				continue
			}
			in.WriteString(*msg.Data)
			for {
				s := in.String()
				line, rest, ok := strings.Cut(s, "\n")
				if !ok {
					break
				}
				in.Reset()
				in.WriteString(rest)
				f := strings.Split(line, " ")
				if len(f) != 4 || f[0] != "W" {
					t.Errorf("bad request %q", line)
					return
				}
				off, err := strconv.ParseInt(f[2], 10, 64)
				check, err2 := base64.StdEncoding.DecodeString(f[3])
				if err != nil || err2 != nil {
					t.Errorf("bad request %q", line)
					return
				}
				mu.Lock()
				k := 0
				for _, q := range reqs {
					if q.conn == n {
						k++
					}
				}
				reqs = append(reqs, request{conn: n, nonce: f[1], off: off, check: string(check)})
				mu.Unlock()
				if k >= len(sc.replies) {
					continue // silence
				}
				rp := sc.replies[k]
				if rp.drop {
					return
				}
				if rp.out != nil && !write(rp.out(f[1])) {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	env := &Env{
		Log:   func(string, ...any) {},
		Phase: func(string) {},
		Stream: func(ctx context.Context, command, ready string) (StreamConn, *bufio.Reader, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > waitReconnectTimeout {
				t.Errorf("stream deadline = %v", deadline)
			}
			tty, r, err := colab.Stream(ctx, &colab.Connection{URL: srv.URL}, command, ready)
			if err != nil {
				return nil, nil, err
			}
			return tty, r, nil
		},
	}
	return env, func() []request {
		mu.Lock()
		defer mu.Unlock()
		return append([]request(nil), reqs...)
	}, &conns
}

func offsets(reqs []request) string {
	var b []string
	for _, q := range reqs {
		b = append(b, fmt.Sprintf("%d:%d", q.conn, q.off))
	}
	return strings.Join(b, " ")
}

// The two failures seen deploying ComfyUI: an answer that came back
// broken, and a new terminal that printed an earlier one's output (an END
// with no START, and an answer to another request) before its own. The
// wait goes on, on a new terminal, from the last log offset confirmed,
// showing every line once.
func TestWaitReadyRecoversFromBrokenTerminals(t *testing.T) {
	c := Comfy{}
	id, check := c.ID(), c.readyCheck(ComfyPort)
	const phase = "@@PHASE installing@@\n"
	const progress = "10%\r"
	first := phase + "Keeping torch\n" + strings.Repeat("\n", waitLogChunk-len(phase)-len("Keeping torch\n")-len(progress)) + progress
	second := "100%\nloading\n"
	size := waitLogChunk + len(second)
	env, reqs, conns := monitorEnv(t, id,
		script{replies: []reply{
			logReply(size, 0, first, "yes"),
			badReply(size, waitLogChunk, second),
			badReply(size, waitLogChunk, second),
		}},
		script{
			stale: "@@END-1:0@@\n" + answerLine("0123456789ab", "ready") + "@@WR",
			replies: []reply{
				{out: func(n string) string {
					return answerLine("0123456789ab", "ready") + logReply(size, waitLogChunk, second, "yes").out(n)
				}},
				{drop: true},
			},
		},
		script{replies: []reply{
			{out: func(n string) string {
				return "garbage\n" + strings.TrimSuffix(logReply(size, size, "", "yes").out(n), "\n") + "\r\n"
			}},
			readyReply(),
		}},
	)
	fastWait(t)
	var phases, logs []string
	env.Phase = func(p string) { phases = append(phases, p) }
	env.Log = func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := waitReady(ctx, env, id, 90*time.Minute, check); err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, l := range logs {
		if !strings.HasPrefix(l, "The terminal stopped answering") {
			shown = append(shown, l)
		}
	}
	if strings.Join(phases, " ") != "installing" || strings.Join(shown, "|") != "Keeping torch|100%|loading" {
		t.Errorf("phases %q, logs %q", phases, logs)
	}
	got := reqs()
	want := fmt.Sprintf("1:0 1:%[1]d 1:%[1]d 2:%[1]d 2:%[2]d 3:%[2]d 3:%[2]d", waitLogChunk, size)
	if offsets(got) != want {
		t.Errorf("requests %s, want %s", offsets(got), want)
	}
	seen := map[string]bool{}
	for _, q := range got {
		if q.check != check || seen[q.nonce] {
			t.Errorf("request %+v: check or nonce reused", q)
		}
		seen[q.nonce] = true
	}
	if conns.Load() != 3 {
		t.Errorf("%d terminals", conns.Load())
	}
	for _, l := range logs {
		for _, q := range got {
			if strings.Contains(l, q.nonce) {
				t.Errorf("log %q shows a nonce", l)
			}
		}
	}
}

// fastWait makes requests time out, and the waits between polls and
// terminals, short.
func fastWait(t *testing.T) {
	oldTimeout, oldPoll, oldReopen := waitProbeTimeout, waitPollInterval, waitReopenDelay
	waitProbeTimeout, waitPollInterval, waitReopenDelay = 300*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { waitProbeTimeout, waitPollInterval, waitReopenDelay = oldTimeout, oldPoll, oldReopen })
}

// A plugin that stopped gives its log's end, from where it was read to.
func TestWaitReadyKeepsStoppedTail(t *testing.T) {
	const id, check = "p", "false"
	const last = "\n20%\rfatal: dependency install failed"
	tail := strings.Repeat("\n", waitLogChunk-len(last)) + last
	env, _, _ := monitorEnv(t, id, script{replies: []reply{
		logReply(3*waitLogChunk, 2*waitLogChunk, tail, "no"),
	}})
	if err := waitReady(t.Context(), env, id, 5*time.Second, check); err == nil || err.Error() != "the plugin stopped: fatal: dependency install failed" {
		t.Fatalf("waitReady error = %v", err)
	}
}

// Terminals that keep failing end the wait with an error, for the caller
// to give the runtime back, after a bounded number of them.
func TestWaitReadyGivesUp(t *testing.T) {
	const id, check = "p", "false"
	fastWait(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	bad := script{replies: []reply{badReply(1, 0, "x"), badReply(1, 0, "x")}}
	var scripts []script
	for range waitMaxFails {
		scripts = append(scripts, bad)
	}
	env, reqs, conns := monitorEnv(t, id, scripts...)
	if err := waitReady(ctx, env, id, 90*time.Minute, check); !errors.Is(err, errBadAnswer) || err.Error() != "invalid readiness probe response" {
		t.Fatalf("waitReady error = %v", err)
	}
	if conns.Load() != waitMaxFails || len(reqs()) != waitMaxFails*waitMaxMisses {
		t.Errorf("%d terminals, %d requests", conns.Load(), len(reqs()))
	}

	var dials int
	env.Stream = func(context.Context, string, string) (StreamConn, *bufio.Reader, error) {
		dials++
		return nil, nil, errors.New("colab: terminal: 502")
	}
	if err := waitReady(ctx, env, id, 90*time.Minute, check); err == nil || !strings.Contains(err.Error(), "502") || dials != waitMaxFails {
		t.Fatalf("waitReady error = %v after %d dials", err, dials)
	}
}

// Past its time the wait ends, even with a terminal that never answers.
func TestWaitReadyTimesOut(t *testing.T) {
	const id, check = "p", "false"
	env, _, _ := monitorEnv(t, id, script{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := waitReady(ctx, env, id, 500*time.Millisecond, check); err == nil || err.Error() != "the plugin didn't get ready in 500ms" {
		t.Fatalf("waitReady error = %v", err)
	}
}

func TestParseWaitAnswer(t *testing.T) {
	line := logReply(10, 4, "abcdef", "yes").out("n")
	rest := strings.TrimPrefix(line, "@@WR n ")
	if a, ok := parseWaitAnswer(rest, 4); !ok || a.size != 10 || a.from != 4 || string(a.data) != "abcdef" || !a.running {
		t.Errorf("good answer: %+v %v", a, ok)
	}
	for name, bad := range map[string]string{
		"cut":      rest[:len(rest)-4],
		"checksum": strings.Replace(rest, "yes", "no ", 1),
		"range":    strings.TrimPrefix(logReply(10, 4, "abc", "yes").out("n"), "@@WR n "),
		"empty":    "",
	} {
		if _, ok := parseWaitAnswer(bad, 4); ok {
			t.Errorf("%s answer taken", name)
		}
	}
}
