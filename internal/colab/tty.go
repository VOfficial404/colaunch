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
	wmu  chan struct{}
	done chan struct{}
	once sync.Once
}

// lockTerminal serializes terminal commands and writes within their deadlines.
func lockTerminal(ctx context.Context, gate chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	}
}

const ttyPath = "/colab/tty"

// DialTTY opens a terminal on the runtime.
func DialTTY(ctx context.Context, conn *Connection) (*TTY, error) {
	return dialTTY(ctx, conn, nil)
}

func dialTTY(ctx context.Context, conn *Connection, client *http.Client) (*TTY, error) {
	u, err := proxyWebSocketURL(conn, ttyPath)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set(proxyTokenHeader, conn.Token)
	h.Set(clientAgentHeader, ClientAgent)
	ws, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: h, HTTPClient: proxyWebSocketClient(client)})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if resp != nil && resp.StatusCode >= 300 && resp.StatusCode <= 599 {
			return nil, fmt.Errorf("colab: terminal: %w", &APIError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)})
		}
		return nil, fmt.Errorf("colab: terminal: %w", proxyWebSocketError(ctx, err))
	}
	ws.SetReadLimit(16 << 20)
	pr, pw := io.Pipe()
	t := &TTY{ws: ws, r: pr, wmu: make(chan struct{}, 1), done: make(chan struct{})}
	go t.readLoop(pw)
	go t.pingLoop()
	t.resize(ctx, 200, 50)
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
	return t.WriteStringContext(context.Background(), s)
}

// WriteStringContext types s into the terminal within ctx's deadline.
func (t *TTY) WriteStringContext(ctx context.Context, s string) error {
	if err := lockTerminal(ctx, t.wmu); err != nil {
		return err
	}
	defer func() { <-t.wmu }()
	// Small messages: proxies limit what they read at once, Colab's too
	// perhaps, and 32 KiB is a common limit.
	const chunk = 8 << 10
	for len(s) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(len(s), chunk)
		b, _ := json.Marshal(map[string]string{"data": s[:n]})
		writeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := t.ws.Write(writeCtx, websocket.MessageText, b)
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
	t.resize(context.Background(), cols, rows)
}

func (t *TTY) resize(ctx context.Context, cols, rows int) {
	b, _ := json.Marshal(map[string]int{"cols": cols, "rows": rows})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if lockTerminal(ctx, t.wmu) != nil {
		return
	}
	defer func() { <-t.wmu }()
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
	mu  chan struct{} // one command at a time
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
	return openShell(ctx, conn, false)
}

// OpenShellAfterMux recovers the shared pane from a mux whose WebSocket
// detached without ending its foreground process.
func OpenShellAfterMux(ctx context.Context, conn *Connection) (*Shell, error) {
	return openShell(ctx, conn, true)
}

func openShell(ctx context.Context, conn *Connection, afterMux bool) (*Shell, error) {
	tty, err := DialTTY(ctx, conn)
	if err != nil {
		return nil, err
	}
	s := newShell(tty)
	go s.pump()
	if err := tty.prepare(ctx, afterMux); err != nil {
		tty.Close()
		return nil, err
	}
	select {
	case <-ctx.Done():
		tty.Close()
		return nil, ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	if err := tty.WriteStringContext(ctx, "stty -echo -onlcr 2>/dev/null; PS1=; PS2=; unset PROMPT_COMMAND\n"); err != nil {
		tty.Close()
		return nil, err
	}
	// One marker of a nonce of its own tells the shell reads commands:
	// typed, it has quotes inside, so the terminal's echo doesn't match.
	hello := randHex(8)
	if err := tty.WriteStringContext(ctx, "echo @@H\"\"ELLO-"+hello+"@@\n"); err != nil {
		tty.Close()
		return nil, err
	}
	if _, _, err := s.wait(ctx, "@@HELLO-"+hello+"@@\n", false); err != nil {
		tty.Close()
		return nil, err
	}
	return s, nil
}

// A mux that survived a WebSocket drop may still read the shared pane in
// raw mode. Send its quit message before typing shell commands. If bash
// already owns the pane, the line is discarded with the startup output.
func (t *TTY) prepare(ctx context.Context, afterMux bool) error {
	if afterMux {
		if err := t.WriteStringContext(ctx, "@@COLAUNCH-MUX-QUIT@@\n"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return t.WriteStringContext(ctx, prelude)
}

func newShell(tty *TTY) *Shell {
	s := &Shell{tty: tty, mu: make(chan struct{}, 1), tmp: "/tmp/.colaunch-" + randHex(8)}
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
	return s.waitReply(ctx, marker, code, "")
}

func (s *Shell) waitReply(ctx context.Context, marker string, code bool, prefix string) ([]byte, int, error) {
	stop := context.AfterFunc(ctx, func() {
		s.bmu.Lock()
		s.cond.Broadcast()
		s.bmu.Unlock()
	})
	defer stop()
	m, p := []byte(marker), []byte(prefix)
	frameScan := 0
	s.bmu.Lock()
	defer s.bmu.Unlock()
	for {
		if i, j, status := s.find(m, code); i >= 0 {
			before := bytes.Clone(s.buf.Bytes()[:i])
			s.buf.Next(j)
			s.scan = 0
			return before, status, nil
		}
		if len(p) > 0 {
			data := s.buf.Bytes()
			if i := bytes.Index(data[frameScan:], p); i >= 0 {
				frameScan += i
				if end := bytes.IndexByte(data[frameScan+len(p):], '\n'); end >= 0 {
					end += frameScan + len(p) + 1
					frame := bytes.Clone(data[frameScan:end])
					s.buf.Next(end)
					s.scan = 0
					return frame, -1, nil
				}
			} else {
				frameScan = max(frameScan, len(data)-len(p)+1)
			}
		}
		if s.err != nil {
			return nil, -1, fmt.Errorf("%w: %w", ErrTerminalClosed, s.err)
		}
		if ctx.Err() != nil {
			return nil, -1, ctx.Err()
		}
		s.cond.Wait()
	}
}

// Errors identifying a closed terminal and a command that never signaled readiness.
var (
	ErrTerminalClosed   = errors.New("colab: the terminal closed")
	ErrTerminalNotReady = errors.New("colab: the terminal command didn't start")
)

// ErrMissingStart means Run consumed the command's complete END marker,
// but its output had no complete START marker.
var ErrMissingStart = errors.New("colab: missing command start marker")

type missingStartError int

func (n missingStartError) Error() string {
	return fmt.Sprintf("colab: the output of command %d has no start", n)
}

func (missingStartError) Unwrap() error { return ErrMissingStart }

// Run runs a bash command and returns what it printed, stdout and stderr
// together, and its exit status.
func (s *Shell) Run(ctx context.Context, cmd string) (string, int, error) {
	return s.run(ctx, cmd, "")
}

// RunFramed runs a read-only command whose reply contains a unique prefix.
// It can return a complete line before END arrives, with status -1. The
// caller must validate its frame before using it.
func (s *Shell) RunFramed(ctx context.Context, cmd, prefix string) (string, int, error) {
	return s.run(ctx, cmd, prefix)
}

func (s *Shell) run(ctx context.Context, cmd, prefix string) (string, int, error) {
	if err := lockTerminal(ctx, s.mu); err != nil {
		return "", -1, err
	}
	defer func() { <-s.mu }()
	s.n++
	n := s.n
	enc := base64.StdEncoding.EncodeToString([]byte(cmd))
	// Lines a canonical terminal reads are at most 1024 bytes on macOS and
	// 4096 on Linux: longer commands go up as a file.
	if len(enc) > 700 {
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
	if err := s.tty.WriteStringContext(ctx, line); err != nil {
		return "", -1, err
	}
	before, code, err := s.waitReply(ctx, fmt.Sprintf("@@END-%d:", n), true, prefix)
	if err != nil {
		return "", -1, err
	}
	if code < 0 {
		return string(before), code, nil
	}
	startMark := []byte(fmt.Sprintf("@@START-%d@@\n", n))
	if i := bytes.LastIndex(before, startMark); i >= 0 {
		return string(before[i+len(startMark):]), code, nil
	}
	if prefix != "" && bytes.Count(before, []byte(prefix)) == 1 {
		return string(before), code, nil
	}
	return "", -1, missingStartError(n)
}

// Upload writes data to path on the runtime, making its directory.
func (s *Shell) Upload(ctx context.Context, path string, data []byte) error {
	if err := lockTerminal(ctx, s.mu); err != nil {
		return err
	}
	defer func() { <-s.mu }()
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
	// The paths go in once, as variables: canonical terminals cut lines
	// short, at 1024 bytes on macOS.
	vars := fmt.Sprintf("__d=%s; __f=%s; __p=%s; ", dir, q, part)
	prep := "mkdir -p \"$__d\""
	if private {
		prep = "(umask 077; mkdir -p \"$__d\") && [ -O \"$__d\" ] && [ ! -L \"$__d\" ]"
	}
	cmd := fmt.Sprintf("%s%s; __s=$?; [ $__s = 0 ] && stty raw -echo; echo \"@@R\"\"EADY-%d:$__s@@\"; "+
		"if [ $__s = 0 ]; then (set -C; head -c %d | base64 -d > \"$__p\"); __s=$?; stty sane -echo -onlcr; "+
		// A directory in the way would take the file inside it, as mv does
		// without GNU's -T, which BSD's mv lacks: refuse it instead.
		"if [ $__s = 0 ]; then if [ -d \"$__f\" ] && [ ! -L \"$__f\" ]; then __s=1; else mv -f \"$__p\" \"$__f\"; __s=$?; fi; fi; [ $__s = 0 ] || rm -f \"$__p\"; fi; "+
		"echo \"@@D\"\"ONE-%d:$__s@@\"\n",
		vars, prep, n, len(enc), n)
	if err := s.tty.WriteStringContext(ctx, cmd); err != nil {
		return err
	}
	_, code, err := s.wait(ctx, fmt.Sprintf("@@READY-%d:", n), true)
	if err != nil {
		return err
	}
	if code == 0 {
		if err := s.tty.WriteStringContext(ctx, enc); err != nil {
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

// Detach waits for any command using the terminal before closing it.
func (s *Shell) Detach(ctx context.Context) error {
	if err := lockTerminal(ctx, s.mu); err != nil {
		return err
	}
	defer func() { <-s.mu }()
	return s.Close()
}

// Done is closed once the shell's terminal is.
func (s *Shell) Done() <-chan struct{} { return s.tty.Done() }

// Stream starts command in a terminal of its own and hands back the
// terminal once the command prints ready on a line, for protocols spoken
// over its input and output. The terminal's input is raw, so what is sent
// reaches the command as it is.
func Stream(ctx context.Context, conn *Connection, command, ready string) (*TTY, *bufio.Reader, error) {
	return openStream(ctx, conn, "exec "+command, ready, false, nil)
}

// StreamMux starts the fallback's mux as a child of bash. When the mux
// receives its quit message, bash restores the pane's terminal settings.
func StreamMux(ctx context.Context, conn *Connection, command, ready string) (*TTY, *bufio.Reader, error) {
	return openStream(ctx, conn, command+"; stty sane -echo -onlcr", ready, true, nil)
}

func openStream(ctx context.Context, conn *Connection, command, ready string, afterMux bool, client *http.Client) (*TTY, *bufio.Reader, error) {
	tty, err := dialTTY(ctx, conn, client)
	if err != nil {
		return nil, nil, err
	}
	if err := tty.prepare(ctx, afterMux); err != nil {
		tty.Close()
		return nil, nil, err
	}
	select {
	case <-ctx.Done():
		tty.Close()
		return nil, nil, ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	if err := tty.WriteStringContext(ctx, "stty raw -echo; "+command+"\n"); err != nil {
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
			if isReadyLine(line, ready) {
				found <- nil
				return
			}
		}
	}()
	select {
	case err := <-found:
		if err != nil {
			tty.Close()
			return nil, nil, fmt.Errorf("colab: the terminal command ended before it was ready: %w: %w", ErrTerminalClosed, err)
		}
	case <-ctx.Done():
		tty.Close()
		return nil, nil, ctx.Err()
	case <-time.After(45 * time.Second):
		tty.Close()
		return nil, nil, ErrTerminalNotReady
	}
	return tty, r, nil
}

// isReadyLine tells whether a line the terminal printed is the command's
// ready line: ready ending it, alone or after bytes an earlier terminal
// printed without ending their line. The command's own line, echoed as
// typed, has ready inside quotes, so it doesn't end with it.
func isReadyLine(line, ready string) bool {
	return strings.HasSuffix(strings.TrimRight(line, " \t\r\n"), ready)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
