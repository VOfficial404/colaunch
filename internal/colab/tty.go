package colab

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// TTY is a terminal on a runtime, the one Colab's terminal shows, over the
// proxy's /colab/tty WebSocket. Its messages are JSON: {"data": "..."} both
// ways, and {"cols": n, "rows": n} to resize.
type TTY struct {
	ws   *websocket.Conn
	r    *io.PipeReader
	wmu  sync.Mutex
	done chan struct{}
	once sync.Once
}

// DialTTY opens a terminal on the runtime.
func DialTTY(ctx context.Context, conn *Connection) (*TTY, error) {
	u := strings.TrimSuffix(conn.URL, "/") + "/colab/tty"
	u = "ws" + strings.TrimPrefix(u, "http")
	h := http.Header{}
	h.Set(proxyTokenHeader, conn.Token)
	h.Set(clientAgentHeader, ClientAgent)
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return nil, fmt.Errorf("colab: terminal: %w", err)
	}
	ws.SetReadLimit(16 << 20)
	pr, pw := io.Pipe()
	t := &TTY{ws: ws, r: pr, done: make(chan struct{})}
	go t.readLoop(pw)
	go t.pingLoop()
	t.Resize(200, 50)
	return t, nil
}

func (t *TTY) readLoop(pw *io.PipeWriter) {
	defer t.Close()
	for {
		_, data, err := t.ws.Read(context.Background())
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		var m struct {
			Data *string `json:"data"`
		}
		if json.Unmarshal(data, &m) != nil || m.Data == nil {
			continue
		}
		if _, err := pw.Write([]byte(*m.Data)); err != nil {
			return
		}
	}
}

func (t *TTY) pingLoop() {
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := t.ws.Ping(ctx)
			cancel()
			if err != nil {
				t.Close()
				return
			}
		}
	}
}

// Read reads what the terminal prints.
func (t *TTY) Read(p []byte) (int, error) { return t.r.Read(p) }

// WriteString types s into the terminal.
func (t *TTY) WriteString(s string) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	// Small messages: proxies limit what they read at once, Colab's too
	// perhaps, and 32 KiB is a common limit.
	const chunk = 8 << 10
	for len(s) > 0 {
		n := min(len(s), chunk)
		b, _ := json.Marshal(map[string]string{"data": s[:n]})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := t.ws.Write(ctx, websocket.MessageText, b)
		cancel()
		if err != nil {
			return err
		}
		s = s[n:]
	}
	return nil
}

// Resize sets the terminal's size.
func (t *TTY) Resize(cols, rows int) {
	b, _ := json.Marshal(map[string]int{"cols": cols, "rows": rows})
	t.wmu.Lock()
	defer t.wmu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.ws.Write(ctx, websocket.MessageText, b)
}

// Done is closed once the terminal is.
func (t *TTY) Done() <-chan struct{} { return t.done }

// Close closes the terminal, which ends what runs in its foreground.
func (t *TTY) Close() error {
	t.once.Do(func() {
		close(t.done)
		t.ws.Close(websocket.StatusNormalClosure, "")
		t.r.Close()
	})
	return nil
}

// Shell runs commands on a runtime through a terminal of its own, one at a
// time. Its output is told from the terminal's other output by markers the
// command prints, written with an empty pair of quotes inside, so that the
// terminal echoing the command doesn't print them.
type Shell struct {
	tty *TTY
	mu  sync.Mutex // one command at a time
	n   int
	// tmp is a folder of the shell's own on the runtime, made on first
	// use, for commands too long for a line.
	tmp string

	bmu  sync.Mutex
	cond *sync.Cond
	buf  bytes.Buffer
	// scan is where in buf the marker waited for can start at the
	// earliest: what is before it was looked through already.
	scan int
	err  error
}

// prelude makes the terminal plain: a bash that reads lines without
// readline, so that it echoes nothing once the terminal doesn't.
const prelude = "exec env TERM=dumb PS1= PS2= HISTFILE=/dev/null bash --noediting --norc --noprofile\n"

// OpenShell opens a terminal on the runtime and readies it for commands.
func OpenShell(ctx context.Context, conn *Connection) (*Shell, error) {
	tty, err := DialTTY(ctx, conn)
	if err != nil {
		return nil, err
	}
	s := newShell(tty)
	go s.pump()
	if err := tty.WriteString(prelude); err != nil {
		tty.Close()
		return nil, err
	}
	time.Sleep(300 * time.Millisecond)
	if err := tty.WriteString("stty -echo -onlcr 2>/dev/null; PS1=; PS2=; unset PROMPT_COMMAND\n"); err != nil {
		tty.Close()
		return nil, err
	}
	if _, _, err := s.Run(ctx, "true"); err != nil {
		tty.Close()
		return nil, err
	}
	return s, nil
}

func newShell(tty *TTY) *Shell {
	s := &Shell{tty: tty, tmp: "/tmp/.colaunch-" + randHex(8)}
	s.cond = sync.NewCond(&s.bmu)
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Shell) pump() {
	b := make([]byte, 32<<10)
	for {
		n, err := s.tty.Read(b)
		s.bmu.Lock()
		// Drop the terminal's carriage returns without copying the chunk.
		for chunk := b[:n]; len(chunk) > 0; {
			i := bytes.IndexByte(chunk, '\r')
			if i < 0 {
				s.buf.Write(chunk)
				break
			}
			s.buf.Write(chunk[:i])
			chunk = chunk[i+1:]
		}
		if err != nil {
			s.err = err
		}
		s.cond.Broadcast()
		s.bmu.Unlock()
		if err != nil {
			return
		}
	}
}

// find looks for marker in what the terminal printed and wasn't looked
// through yet, followed, with code, by digits and "@@". It returns where
// the marker starts and ends, and the digits' value, or -1 while there is
// none yet. Call it with bmu held.
func (s *Shell) find(marker []byte, code bool) (start, end, status int) {
	b := s.buf.Bytes()
	for {
		// A marker may have started in the bytes looked through last,
		// its end not printed yet.
		from := s.scan
		i := bytes.Index(b[from:], marker)
		if i < 0 {
			s.scan = max(from, len(b)-len(marker)+1)
			return -1, -1, 0
		}
		i += from
		j := i + len(marker)
		if !code {
			return i, j, 0
		}
		k := j
		for k < len(b) && b[k] >= '0' && b[k] <= '9' {
			k++
		}
		if k == len(b) || (k == len(b)-1 && b[k] == '@') {
			// The rest isn't printed yet.
			s.scan = i
			return -1, -1, 0
		}
		if k > j && b[k] == '@' && b[k+1] == '@' {
			status, err := strconv.Atoi(string(b[j:k]))
			if err == nil {
				return i, k + 2, status
			}
		}
		// Not a marker after all: look past it.
		s.scan = i + 1
	}
}

// wait waits until the terminal prints marker (and, with code, digits and
// "@@" after it), and returns what it printed before, and the digits.
// What it printed up to the marker's end is dropped.
func (s *Shell) wait(ctx context.Context, marker string, code bool) ([]byte, int, error) {
	stop := context.AfterFunc(ctx, func() {
		s.bmu.Lock()
		s.cond.Broadcast()
		s.bmu.Unlock()
	})
	defer stop()
	m := []byte(marker)
	s.bmu.Lock()
	defer s.bmu.Unlock()
	for {
		if i, j, status := s.find(m, code); i >= 0 {
			before := bytes.Clone(s.buf.Bytes()[:i])
			s.buf.Next(j)
			s.scan = 0
			return before, status, nil
		}
		if s.err != nil {
			return nil, -1, fmt.Errorf("colab: the terminal closed: %w", s.err)
		}
		if ctx.Err() != nil {
			return nil, -1, ctx.Err()
		}
		s.cond.Wait()
	}
}

// Run runs a bash command and returns what it printed, stdout and stderr
// together, and its exit status.
func (s *Shell) Run(ctx context.Context, cmd string) (string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	n := s.n
	enc := base64.StdEncoding.EncodeToString([]byte(cmd))
	if len(enc) > 3000 {
		// Lines a terminal reads are at most 4095 bytes long.
		p := fmt.Sprintf("%s/cmd-%d.sh", s.tmp, n)
		if err := s.upload(ctx, p, []byte(cmd), true); err != nil {
			return "", -1, err
		}
		enc = base64.StdEncoding.EncodeToString([]byte("bash " + p + "; __s=$?; rm -f " + p + "; (exit $__s)"))
	}
	// Typed, the markers have quotes inside; printed, they have none. The
	// command runs in a subshell, so that its exit or cd leaves the shell be.
	line := fmt.Sprintf("echo @@S\"\"TART-%d@@; (eval \"$(echo %s | base64 -d)\") </dev/null 2>&1; echo \"@@E\"\"ND-%d:$?@@\"\n", n, enc, n)
	if err := s.tty.WriteString(line); err != nil {
		return "", -1, err
	}
	before, code, err := s.wait(ctx, fmt.Sprintf("@@END-%d:", n), true)
	if err != nil {
		return "", -1, err
	}
	startMark := []byte(fmt.Sprintf("@@START-%d@@\n", n))
	i := bytes.LastIndex(before, startMark)
	if i < 0 {
		return "", -1, fmt.Errorf("colab: the output of command %d has no start", n)
	}
	return string(before[i+len(startMark):]), code, nil
}

// Upload writes data to path on the runtime, making its directory.
func (s *Shell) Upload(ctx context.Context, path string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.upload(ctx, path, data, false)
}

// upload switches the terminal to raw input, which has no limit on the
// length of its lines, for head to read exactly the bytes sent. They go to
// a file of a name no one else has, next to dst, made only if it doesn't
// exist, and are moved to dst once whole. With private, dst's folder is
// made for the user alone, and must be theirs.
func (s *Shell) upload(ctx context.Context, dst string, data []byte, private bool) error {
	n := s.n
	enc := base64.StdEncoding.EncodeToString(data)
	q := shellQuote(dst)
	dir := shellQuote(pathpkg.Dir(dst))
	part := shellQuote(dst + ".colaunch-" + randHex(8) + ".part")
	prep := "mkdir -p " + dir
	if private {
		prep = fmt.Sprintf("(umask 077; mkdir -p %s) && [ -O %s ] && [ ! -L %s ]", dir, dir, dir)
	}
	cmd := fmt.Sprintf("%s; __s=$?; [ $__s = 0 ] && stty raw -echo; echo \"@@R\"\"EADY-%d:$__s@@\"; "+
		"if [ $__s = 0 ]; then (set -C; head -c %d | base64 -d > %s); __s=$?; stty sane -echo -onlcr; "+
		// A directory in the way would take the file inside it, as mv does
		// without GNU's -T, which BSD's mv lacks: refuse it instead.
		"if [ $__s = 0 ]; then if [ -d %s ] && [ ! -L %s ]; then __s=1; else mv -f %s %s; __s=$?; fi; fi; [ $__s = 0 ] || rm -f %s; fi; "+
		"echo \"@@D\"\"ONE-%d:$__s@@\"\n",
		prep, n, len(enc), part, q, q, part, q, part, n)
	if err := s.tty.WriteString(cmd); err != nil {
		return err
	}
	_, code, err := s.wait(ctx, fmt.Sprintf("@@READY-%d:", n), true)
	if err != nil {
		return err
	}
	if code == 0 {
		if err := s.tty.WriteString(enc); err != nil {
			return err
		}
	}
	_, done, err := s.wait(ctx, fmt.Sprintf("@@DONE-%d:", n), true)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("colab: making the folder of %s failed (%d)", dst, code)
	}
	if done != 0 {
		return fmt.Errorf("colab: writing %s failed (%d)", dst, done)
	}
	return nil
}

// Close closes the shell's terminal.
func (s *Shell) Close() error { return s.tty.Close() }

// Done is closed once the shell's terminal is.
func (s *Shell) Done() <-chan struct{} { return s.tty.Done() }

// Stream starts command in a terminal of its own and hands back the
// terminal once the command prints ready on a line, for protocols spoken
// over its input and output. The terminal's input is raw, so what is sent
// reaches the command as it is.
func Stream(ctx context.Context, conn *Connection, command, ready string) (*TTY, *bufio.Reader, error) {
	tty, err := DialTTY(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	if err := tty.WriteString(prelude); err != nil {
		tty.Close()
		return nil, nil, err
	}
	time.Sleep(300 * time.Millisecond)
	if err := tty.WriteString("stty raw -echo; exec " + command + "\n"); err != nil {
		tty.Close()
		return nil, nil, err
	}
	r := bufio.NewReaderSize(tty, 256<<10)
	found := make(chan error, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				found <- err
				return
			}
			if strings.TrimSpace(line) == ready {
				found <- nil
				return
			}
		}
	}()
	select {
	case err := <-found:
		if err != nil {
			tty.Close()
			return nil, nil, fmt.Errorf("colab: %s ended before it was ready: %w", command, err)
		}
	case <-ctx.Done():
		tty.Close()
		return nil, nil, ctx.Err()
	case <-time.After(45 * time.Second):
		tty.Close()
		return nil, nil, errors.New("colab: " + command + " didn't start")
	}
	return tty, r, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
