package tunnel

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAgent stands in for agent.py: the test reads what the app sends and
// writes what the agent would.
type fakeAgent struct {
	sent chan string // lines the app sent
	w    *io.PipeWriter
	once sync.Once
}

func (a *fakeAgent) WriteString(s string) error {
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		a.sent <- l
	}
	return nil
}

func (a *fakeAgent) Close() error { a.once.Do(func() { a.w.Close() }); return nil }

// say writes a line as the agent. It blocks while the mux doesn't read.
func (a *fakeAgent) say(format string, args ...any) error {
	_, err := fmt.Fprintf(a.w, format+"\n", args...)
	return err
}

// expect waits for a line the app sends that starts with prefix, skipping
// pings.
func (a *fakeAgent) expect(t *testing.T, prefix string) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l := <-a.sent:
			if l == "P" {
				continue
			}
			if !strings.HasPrefix(l, prefix) {
				t.Fatalf("app sent %.40q, want %q", l, prefix)
			}
			return l
		case <-timeout:
			t.Fatalf("app didn't send %q", prefix)
			return ""
		}
	}
}

func fakeMux(t *testing.T, tick time.Duration) (*Mux, *fakeAgent) {
	r, w := io.Pipe()
	a := &fakeAgent{sent: make(chan string, 1024), w: w}
	m := newMux(a, bufio.NewReader(r), tick)
	t.Cleanup(func() { m.Close() })
	return m, a
}

// dialFake opens a stream as the agent would answer.
func dialFake(t *testing.T, m *Mux, a *fakeAgent) (*Stream, string) {
	t.Helper()
	got := make(chan *Stream, 1)
	go func() {
		s, err := m.Dial(80)
		if err != nil {
			t.Error(err)
		}
		got <- s
	}()
	id := strings.Fields(a.expect(t, "O "))[1]
	a.say("K %s", id)
	return <-got, id
}

func setLimits(t *testing.T, buf int, stall, half time.Duration) {
	ob, ost, oh := MaxBuffer, StallTimeout, HalfCloseTimeout
	MaxBuffer, StallTimeout, HalfCloseTimeout = buf, stall, half
	t.Cleanup(func() { MaxBuffer, StallTimeout, HalfCloseTimeout = ob, ost, oh })
}

// A stream holds MaxBuffer at the most: past it the mux stops reading
// until the stream's reader takes some, and nothing is lost.
func TestStreamBackpressure(t *testing.T) {
	setLimits(t, 4<<10, 10*time.Second, time.Minute)
	m, a := fakeMux(t, time.Hour)
	s, id := dialFake(t, m, a)
	chunk := bytes.Repeat([]byte("x"), 1000)
	var want bytes.Buffer
	sent := make(chan int, 1)
	go func() {
		n := 0
		for i := range 20 {
			b := append(bytes.Clone(chunk), byte('a'+i))
			want.Write(b)
			if a.say("D %s %s", id, base64.StdEncoding.EncodeToString(b)) != nil {
				break
			}
			n++
		}
		sent <- n
	}()
	time.Sleep(200 * time.Millisecond)
	s.mu.Lock()
	held := s.buf.Len()
	s.mu.Unlock()
	if held > MaxBuffer {
		t.Errorf("the stream holds %d bytes, over %d", held, MaxBuffer)
	}
	select {
	case n := <-sent:
		t.Fatalf("all %d pieces went in without reading", n)
	default:
	}
	got := make([]byte, 20*1001)
	if _, err := io.ReadFull(s, got); err != nil {
		t.Fatal(err)
	}
	<-sent
	if !bytes.Equal(got, want.Bytes()) {
		t.Error("bytes changed or went missing under backpressure")
	}
}

// A reader that stops reading loses its stream after StallTimeout, and the
// other streams go on.
func TestStalledStreamCloses(t *testing.T) {
	setLimits(t, 1<<10, 100*time.Millisecond, time.Minute)
	m, a := fakeMux(t, time.Hour)
	stalled, id := dialFake(t, m, a)
	big := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("y"), 800))
	for range 3 {
		a.say("D %s %s", id, big)
	}
	if l := a.expect(t, "C "); l != "C "+id {
		t.Errorf("app sent %q", l)
	}
	other, id2 := dialFake(t, m, a)
	a.say("D %s %s", id2, base64.StdEncoding.EncodeToString([]byte("hi")))
	b := make([]byte, 2)
	if _, err := io.ReadFull(other, b); err != nil || string(b) != "hi" {
		t.Errorf("the other stream read %q, %v", b, err)
	}
	if _, err := io.ReadAll(stalled); !errors.Is(err, ErrStalled) {
		t.Errorf("the stalled stream ended with %v", err)
	}
}

// A local client that ends its side still hears the answer, and the agent
// hears W; the runtime's side ending with W leaves the other way open.
func TestHalfClose(t *testing.T) {
	setLimits(t, 4<<20, time.Second, 5*time.Second)
	m, a := fakeMux(t, time.Hour)
	f, err := Listen("127.0.0.1:0", 80, m, ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	c, _ := net.Dial("tcp", f.Local)
	defer c.Close()
	id := strings.Fields(a.expect(t, "O "))[1]
	a.say("K %s", id)
	c.Write([]byte("question"))
	c.(*net.TCPConn).CloseWrite()
	if l := a.expect(t, "D "+id); !strings.HasSuffix(l, base64.StdEncoding.EncodeToString([]byte("question"))) {
		t.Errorf("app sent %q", l)
	}
	a.expect(t, "W "+id)
	a.say("D %s %s", id, base64.StdEncoding.EncodeToString([]byte("answer")))
	a.say("C %s", id)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if got, err := io.ReadAll(c); err != nil || string(got) != "answer" {
		t.Errorf("after closing its side, the client read %q, %v", got, err)
	}

	c2, _ := net.Dial("tcp", f.Local)
	defer c2.Close()
	id2 := strings.Fields(a.expect(t, "O "))[1]
	a.say("K %s", id2)
	a.say("D %s %s", id2, base64.StdEncoding.EncodeToString([]byte("hello")))
	a.say("W %s", id2)
	c2.SetReadDeadline(time.Now().Add(5 * time.Second))
	if got, err := io.ReadAll(c2); err != nil || string(got) != "hello" {
		t.Errorf("the client read %q, %v before the runtime's EOF", got, err)
	}
	c2.Write([]byte("more"))
	if l := a.expect(t, "D "+id2); !strings.HasSuffix(l, base64.StdEncoding.EncodeToString([]byte("more"))) {
		t.Errorf("after the runtime's W, app sent %q", l)
	}
}

// Closing a forward closes the connections it carries.
func TestForwardCloseClosesConns(t *testing.T) {
	m, a := fakeMux(t, time.Hour)
	f, err := Listen("127.0.0.1:0", 80, m, ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := net.Dial("tcp", f.Local)
	defer c.Close()
	id := strings.Fields(a.expect(t, "O "))[1]
	a.say("K %s", id)
	for f.Conns() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	f.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("the connection stayed open: %v", err)
	}
	a.expect(t, "C "+id)
	if m.Streams() != 0 {
		t.Errorf("%d streams open", m.Streams())
	}
}

// A mux without streams closes itself after its idle time; one with a
// stream doesn't.
func TestMuxIdle(t *testing.T) {
	m, a := fakeMux(t, 10*time.Millisecond)
	m.SetIdleTimeout(50 * time.Millisecond)
	s, _ := dialFake(t, m, a)
	time.Sleep(150 * time.Millisecond)
	select {
	case <-m.Done():
		t.Fatal("closed with a stream open")
	default:
	}
	s.Close()
	select {
	case <-m.Done():
		if !errors.Is(m.Err(), ErrIdle) {
			t.Errorf("closed with %v", m.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an idle mux stayed open")
	}
}

// With agent.py, which doesn't know W yet, a client that ends its side
// still gets the answer, and the connection closes HalfCloseTimeout after
// the runtime's side went quiet.
func TestHalfCloseThroughAgent(t *testing.T) {
	setLimits(t, 4<<20, time.Second, 500*time.Millisecond)
	m := startMux(t)
	f, err := Listen("127.0.0.1:0", echoServer(t), m, ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := net.Dial("tcp", f.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := bytes.Repeat([]byte("echo "), 20000)
	c.Write(want)
	c.(*net.TCPConn).CloseWrite()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("read %d of %d bytes, %v", len(got), len(want), err)
	}
}
