// Package plugins is what Colaunch runs on a runtime once it's assigned.
// Deploying a model is a plugin like any other: built in, written in Go.
// Users add their own as a folder with a plugin.json and a script (see
// docs/plugins.md and ScriptPlugin).
package plugins

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
)

// Text is a string in each of the app's languages, by tag: "en",
// "zh-Hans", "zh-Hant". A plain string in plugin.json is English for all.
type Text map[string]string

// In returns the text in lang, falling back to Simplified Chinese for
// Traditional and to English for the rest.
func (t Text) In(lang string) string {
	if s := t[lang]; s != "" {
		return s
	}
	if lang == "zh-Hant" {
		if s := t["zh-Hans"]; s != "" {
			return s
		}
	}
	if s := t["en"]; s != "" {
		return s
	}
	for _, s := range t {
		return s
	}
	return ""
}

// FieldKind is how a field is edited.
type FieldKind string

const (
	FieldText     FieldKind = "text"
	FieldPassword FieldKind = "password"
	FieldNumber   FieldKind = "number"
	FieldSelect   FieldKind = "select"
	FieldSwitch   FieldKind = "switch"
)

// Field is a setting a plugin asks for before it runs.
type Field struct {
	Key         string    `json:"key"`
	Label       Text      `json:"label"`
	Help        Text      `json:"help,omitempty"`
	Kind        FieldKind `json:"kind"`
	Default     string    `json:"default,omitempty"`
	Placeholder string    `json:"placeholder,omitempty"`
	Options     []string  `json:"options,omitempty"`
	Required    bool      `json:"required,omitempty"`
	// Advanced fields show under "Advanced", closed.
	Advanced bool `json:"advanced,omitempty"`
}

// PortKind tells the app what a forwarded port serves.
type PortKind string

const (
	// PortOpenAI serves an OpenAI-compatible API under /v1, and may have
	// a web page at /.
	PortOpenAI PortKind = "openai"
	// PortWeb serves a web page.
	PortWeb PortKind = "web"
	// PortComfy serves ComfyUI: its web page at /, and its API, which
	// the app generates images and videos with (internal/comfy).
	PortComfy PortKind = "comfyui"
	// PortTCP is anything else.
	PortTCP PortKind = "tcp"
)

// Port is a port of the runtime a plugin serves, which the app forwards
// to 127.0.0.1.
type Port struct {
	Name   string   `json:"name"`
	Remote int      `json:"remote"`
	Local  int      `json:"local"`
	Kind   PortKind `json:"kind"`
	// Path is where a web page is, as "/".
	Path string `json:"path,omitempty"`
}

// Result is what a plugin started.
type Result struct {
	Ports []Port
	// Model is what OpenAI clients name in their requests.
	Model string
	// APIKey is the key the API wants, "" for none.
	APIKey string
	// Info is shown beside the runtime, in order.
	Info [][2]string
}

// Hardware is a plugin's advice on the machine to assign.
type Hardware struct {
	Variant colab.Variant
	// MinMemory is the memory, in bytes, the plugin's work needs.
	MinMemory int64
}

// Env is what a plugin's Deploy works with.
type Env struct {
	Values  map[string]string
	Shell   colab.Transport
	Spec    colab.Spec
	Status  *agent.Status
	Lang    string
	HFToken string
	// Settings are app-wide settings plugins may read, as the address of
	// prebuilt llama.cpp.
	Settings map[string]string
	// Log adds a line to the deployment's log.
	Log func(format string, args ...any)
	// Phase tells the app what the plugin is doing now, to show.
	Phase func(phase string)
	// Stream starts a live monitor command in its own SSH channel when available.
	Stream func(ctx context.Context, command, ready string) (StreamConn, *bufio.Reader, error)
	// Control retrieves the current shared connection for a one-shot probe.
	Control func(ctx context.Context) (colab.Transport, error)
	// ReadWaitStatus reads the detached installer's snapshot without a terminal.
	ReadWaitStatus func(ctx context.Context, id, nonce string) (colab.WaitStatus, error)
}

// StreamConn is the input of a terminal Env.Stream opened.
type StreamConn interface {
	WriteStringContext(ctx context.Context, s string) error
	Close() error
}

// Plugin is something to run on a runtime.
type Plugin interface {
	ID() string
	Name() Text
	Description() Text
	Fields() []Field
	// Validate checks the values before a runtime is assigned, which
	// costs money, and may advise on the machine.
	Validate(ctx context.Context, values map[string]string, hfToken string) (*Hardware, error)
	// Deploy runs the plugin on the runtime, returning once what it
	// started serves.
	Deploy(ctx context.Context, env *Env) (*Result, error)
	// Stop stops it, leaving the runtime assigned.
	Stop(ctx context.Context, sh colab.Transport) error
}

var (
	mu       sync.RWMutex
	registry = map[string]Plugin{}
)

// Register adds a plugin. A plugin with the same ID, built in or loaded
// before, stays, and the new one is refused.
func Register(p Plugin) error {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := registry[p.ID()]; ok {
		return fmt.Errorf("plugins: a plugin with the id %q is already registered", p.ID())
	}
	registry[p.ID()] = p
	return nil
}

// Unregister removes a plugin.
func Unregister(id string) {
	mu.Lock()
	delete(registry, id)
	mu.Unlock()
}

// Get returns a plugin by its ID.
func Get(id string) Plugin {
	mu.RLock()
	defer mu.RUnlock()
	return registry[id]
}

// All returns the plugins, the built-in ones first.
func All() []Plugin {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Plugin, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		bi, bj := isBuiltin(out[i]), isBuiltin(out[j])
		if bi != bj {
			return bi
		}
		return out[i].ID() < out[j].ID()
	})
	return out
}

func isBuiltin(p Plugin) bool {
	_, script := p.(*ScriptPlugin)
	return !script
}

// Defaults returns a plugin's fields' default values.
func Defaults(p Plugin) map[string]string {
	v := map[string]string{}
	for _, f := range p.Fields() {
		v[f.Key] = f.Default
	}
	return v
}

// checkRequired checks the fields that must have a value.
func checkRequired(p Plugin, values map[string]string, lang string) error {
	for _, f := range p.Fields() {
		if f.Required && strings.TrimSpace(values[f.Key]) == "" {
			return fmt.Errorf("%s: %s", f.Label.In(lang), "required")
		}
	}
	return nil
}

const (
	waitLogChunk         = 16 << 10
	waitReconnectTimeout = 2 * time.Minute
	// waitMaxMisses is how many bad answers in a row one terminal may give
	// before another is opened, and waitMaxFails how many terminals in a
	// row may fail before waiting gives up.
	waitMaxMisses = 2
	waitMaxFails  = 4
)

// waitProbeTimeout bounds one request: a terminal that doesn't answer in
// it is given up for another.
var waitProbeTimeout = 2 * time.Minute

// waitMonitorBudget bounds opening a monitor terminal while the command
// terminal is there to fall back on. waitShellCheck bounds its first probe.
var (
	waitMonitorBudget = 15 * time.Second
	waitShellCheck    = 20 * time.Second
)

// waitReadyCommand starts the agent's deployment monitor; tests replace it
// to run their own agent.
var waitReadyCommand = agent.WaitReadyCommand

// waitPollInterval is how long to wait between polls once the log is
// read, and waitReopenDelay before opening a terminal after another
// failed.
var (
	waitPollInterval = 4 * time.Second
	waitReopenDelay  = 3 * time.Second
)

var (
	errBadAnswer = errors.New("invalid readiness probe response")
	errNoAnswer  = errors.New("the readiness probe didn't answer")
)

// newProbeToken is a request's nonce; tests replace it.
var newProbeToken = func() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// waitAnswer is a probe's answer: ready, or the plugin's liveness and the
// bytes of its log from from.
type waitAnswer struct {
	ready      bool
	size, from int64
	running    bool
	data       []byte
}

// parseWaitAnswer reads what follows "@@WR nonce " on the agent's line,
// "crc32 payload@@", for the request of the log from off. The checksum
// must match and the payload be whole: its fields well formed and the
// bytes as many as the agent's range says.
func parseWaitAnswer(rest string, off int64) (waitAnswer, bool) {
	rest = strings.TrimRight(rest, "\r\n")
	rest, closed := strings.CutSuffix(rest, "@@")
	sum, payload, ok := strings.Cut(rest, " ")
	if !closed || !ok || len(sum) != 8 || fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(payload))) != sum {
		return waitAnswer{}, false
	}
	if payload == "ready" {
		return waitAnswer{ready: true}, true
	}
	f := strings.Split(payload, " ")
	if len(f) != 5 || f[0] != "log" {
		return waitAnswer{}, false
	}
	size, err1 := strconv.ParseInt(f[1], 10, 64)
	from, err2 := strconv.ParseInt(f[2], 10, 64)
	run := f[3]
	if err1 != nil || err2 != nil || size < 0 || (run != "yes" && run != "no" && run != "unknown") {
		return waitAnswer{}, false
	}
	// Mirror the agent's range: start at off (or zero after a shrink),
	// and take the last unread chunk if the plugin has stopped.
	wantFrom := off
	if off > size {
		wantFrom = 0
	}
	n := min(size-wantFrom, waitLogChunk)
	if run == "no" {
		wantFrom = size - n
	}
	data, err := base64.StdEncoding.DecodeString(f[4])
	if err != nil || from != wantFrom || int64(len(data)) != n {
		return waitAnswer{}, false
	}
	return waitAnswer{size: size, from: from, running: run != "no", data: data}, true
}

// waitMonitor is `agent.py wait-ready` on a terminal of its own: it runs
// the check and reads the log for each request, and answers on one line
// that carries the request's nonce, so that nothing else the terminal
// prints, nor an answer to an earlier request, is taken for it.
type waitMonitor struct {
	conn  StreamConn
	lines chan string
	done  chan struct{}
	err   error // why lines closed
}

func openWaitMonitor(ctx context.Context, env *Env, id string) (*waitMonitor, error) {
	command, ready := waitReadyCommand(id, waitLogChunk, rand.Text())
	conn, r, err := env.Stream(ctx, command, ready)
	if err != nil {
		return nil, err
	}
	m := &waitMonitor{conn: conn, lines: make(chan string, 16), done: make(chan struct{})}
	go func() {
		defer close(m.lines)
		for {
			line, err := r.ReadSlice('\n')
			if err != nil {
				m.err = err
				return
			}
			select {
			case m.lines <- string(line):
			case <-m.done:
				return
			}
		}
	}()
	return m, nil
}

func (m *waitMonitor) close() {
	close(m.done)
	m.conn.Close()
}

func probeRequest(off int64, check string) (nonce, req string) {
	nonce = newProbeToken()
	return nonce, fmt.Sprintf("W %s %d %s\n", nonce, off, base64.StdEncoding.EncodeToString([]byte(check)))
}

// shellProbe asks the same as a monitor's probe over the command terminal:
// the agent answers the one request piped to it and exits. The command
// terminal is the one the plugin was started on, so it reads while a
// dedicated terminal can't be opened.
func shellProbe(ctx context.Context, sh colab.Transport, id string, off int64, check string) (waitAnswer, error) {
	nonce, req := probeRequest(off, check)
	command, _ := waitReadyCommand(id, waitLogChunk, rand.Text())
	probeCtx, cancel := context.WithTimeout(ctx, waitProbeTimeout)
	defer cancel()
	head := "@@WR " + nonce + " "
	cmd := "printf '%s' " + ShellQuote(req) + " | " + command
	var out string
	var code int
	var err error
	if tty, ok := sh.(*colab.Shell); ok {
		out, code, err = tty.RunFramed(probeCtx, cmd, head)
	} else {
		out, code, err = sh.Run(probeCtx, cmd)
	}
	if err == nil && code > 0 {
		return waitAnswer{}, waitProbeExit(code)
	}
	if strings.Count(out, head) == 1 {
		_, rest, _ := strings.Cut(out, head)
		if line, _, complete := strings.Cut(rest, "\n"); complete {
			if a, ok := parseWaitAnswer(line, off); ok {
				return a, nil
			}
		}
		return waitAnswer{}, errBadAnswer
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return waitAnswer{}, errNoAnswer
	}
	if err != nil {
		return waitAnswer{}, err
	}
	return waitAnswer{}, errBadAnswer
}

// probe asks whether the plugin is ready and, if not, for its log from off.
func (m *waitMonitor) probe(ctx context.Context, off int64, check string) (waitAnswer, error) {
	nonce, req := probeRequest(off, check)
	probeCtx, cancel := context.WithTimeout(ctx, waitProbeTimeout)
	defer cancel()
	if err := m.conn.WriteStringContext(probeCtx, req); err != nil {
		return waitAnswer{}, err
	}
	head := "@@WR " + nonce + " "
	for {
		select {
		case line, ok := <-m.lines:
			if !ok {
				return waitAnswer{}, fmt.Errorf("%w: %w", colab.ErrTerminalClosed, m.err)
			}
			i := strings.LastIndex(line, head)
			if i < 0 {
				continue
			}
			if a, ok := parseWaitAnswer(line[i+len(head):], off); ok {
				return a, nil
			}
			return waitAnswer{}, errBadAnswer
		case <-probeCtx.Done():
			if err := ctx.Err(); err != nil {
				return waitAnswer{}, err
			}
			return waitAnswer{}, errNoAnswer
		}
	}
}

type waitProbeExit int

func (code waitProbeExit) Error() string {
	return fmt.Sprintf("the readiness probe exited with status %d", code)
}

// WaitError is waitReady giving up on the terminals it reads the plugin's
// status over: the plugin may still be running. Err is the last failure,
// already reduced by waitTransportError; Monitor and Shell are the last
// outcome on each terminal. Its text keeps the "opening a terminal to
// wait on: " prefix that callers classify by.
type WaitError struct {
	Err            error
	Monitor, Shell string
	Elapsed        time.Duration
}

func (e *WaitError) Error() string {
	return fmt.Sprintf("opening a terminal to wait on: %v (monitor terminal: %s, command terminal: %s, elapsed: %s)",
		e.Err, e.Monitor, e.Shell, e.Elapsed.Round(time.Millisecond))
}

func (e *WaitError) Unwrap() error { return e.Err }

// waitTransportError reports only evidence from recognized errors. Transport
// errors can contain proxy URLs, response bodies, or terminal text.
func waitTransportError(err error) error {
	var api *colab.APIError
	var exit waitProbeExit
	switch {
	case errors.Is(err, errBadAnswer):
		return errBadAnswer
	case errors.Is(err, errNoAnswer):
		return errNoAnswer
	case errors.As(err, &exit):
		return exit
	case errors.As(err, &api) && api.Status >= 100 && api.Status <= 599:
		return fmt.Errorf("HTTP %d", api.Status)
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, colab.ErrSSHClosed):
		return colab.ErrSSHClosed
	case errors.Is(err, colab.ErrTerminalClosed):
		return colab.ErrTerminalClosed
	case errors.Is(err, colab.ErrTerminalNotReady):
		return colab.ErrTerminalNotReady
	case errors.Is(err, colab.ErrMissingStart):
		return colab.ErrMissingStart
	default:
		return errors.New("terminal transport failed")
	}
}

// waitReady polls check every few seconds until it's true, the plugin's
// process ends, or ctx is done. Each poll shows the log's new lines.
//
// Prefer the runtime's status file when a launch has one. If file reads
// fail, continue with the monitor or control terminal. Terminal failures
// return to the file reader on the same runtime without relaunching.
func waitReady(ctx context.Context, env *Env, id string, timeout time.Duration, check, nonce string) error {
	useFiles := nonce != "" && env.ReadWaitStatus != nil
	if env.Stream == nil && env.Shell == nil && !useFiles {
		return errors.New("plugins: no terminal to wait for the plugin on")
	}
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var mon *waitMonitor
	defer func() {
		if mon != nil {
			mon.close()
		}
	}()
	var misses, fails int
	var off int64
	var carry []byte
	var recent []string
	var lastConnectionNote string
	logf := func(format string, args ...any) {
		note := fmt.Sprintf(format, args...)
		if env.Log != nil && note != lastConnectionNote {
			env.Log("%s", note)
			lastConnectionNote = note
		}
	}
	lastMonitor, lastShell := "not tried", "not tried"
	finish := func(err error) error {
		return &WaitError{Err: err, Monitor: lastMonitor, Shell: lastShell, Elapsed: time.Since(start)}
	}
	shell := env.Shell
	if shell == nil {
		lastShell = "unavailable"
	}
	viaShell, firstShellProbe := env.Stream == nil, false
	probeControl := func(ctx context.Context) (waitAnswer, error) {
		if env.Control != nil {
			var err error
			shell, err = env.Control(ctx)
			if err != nil {
				return waitAnswer{}, err
			}
		}
		if shell == nil {
			return waitAnswer{}, colab.ErrTerminalClosed
		}
		return shellProbe(ctx, shell, id, off, check)
	}
	tryFile, fileActive := useFiles, false
	var fileMisses int
	var fileErr error
	for probeCtx.Err() == nil {
		var a waitAnswer
		var err error
		viaFile := false
		if tryFile {
			snap, readErr := env.ReadWaitStatus(probeCtx, id, nonce)
			if probeCtx.Err() != nil {
				break
			}
			if readErr == nil {
				a, readErr = snapshotAnswer(snap, nonce, off, time.Now())
			}
			if fileErr = readErr; fileErr == nil {
				if !fileActive {
					logf("Reading deployment status through the runtime file API.")
					if mon != nil {
						mon.close()
						mon = nil
					}
				}
				viaFile, fileActive, fileMisses = true, true, 0
			} else if fileMisses++; fileMisses < 2 {
				select {
				case <-probeCtx.Done():
				case <-time.After(waitPollInterval):
				}
				continue
			} else {
				if env.Stream == nil && shell == nil && env.Control == nil {
					return finish(fileErr)
				}
				logf("Runtime file status unavailable (%v); trying the terminal.", fileErr)
				tryFile, fileActive = false, false
			}
		}
		if !viaFile && mon == nil && !viaShell {
			if fails > 1 {
				select {
				case <-probeCtx.Done():
					continue
				case <-time.After(waitReopenDelay):
				}
			}
			budget := waitReconnectTimeout
			if shell != nil {
				select {
				case <-shell.Done():
					shell = nil
					lastShell = "closed"
				default:
					budget = waitMonitorBudget
				}
			}
			openCtx, openCancel := context.WithTimeout(probeCtx, budget)
			m, err := openWaitMonitor(openCtx, env, id)
			openCancel()
			if err != nil {
				err = waitTransportError(err)
				lastMonitor = "open failed: " + err.Error()
			}
			if probeCtx.Err() != nil {
				if m != nil {
					m.close()
				}
				break
			}
			if err != nil {
				if shell != nil {
					logf("The monitor terminal didn't connect (%v); reading status over the command terminal.", err)
					viaShell, firstShellProbe, misses = true, true, 0
					continue
				}
				if fails++; fails >= waitMaxFails {
					return finish(errors.Join(err, fileErr))
				}
				tryFile, fileMisses = useFiles, 1
				logf("✗ %v", err)
				continue
			}
			mon, misses = m, 0
		}
		if viaFile {
			// The status snapshot already supplied the answer.
		} else if firstShellProbe {
			firstShellProbe = false
			firstCtx, firstCancel := context.WithTimeout(probeCtx, waitShellCheck)
			a, err = probeControl(firstCtx)
			firstCancel()
		} else if viaShell {
			a, err = probeControl(probeCtx)
		} else {
			a, err = mon.probe(probeCtx, off, check)
		}
		if err != nil {
			err = waitTransportError(err)
			if viaShell {
				lastShell = err.Error()
			} else {
				lastMonitor = err.Error()
			}
		}
		if probeCtx.Err() != nil {
			break
		}
		if err != nil {
			if errors.Is(err, errBadAnswer) {
				if misses++; misses < waitMaxMisses {
					continue
				}
			}
			if viaShell {
				// A bad reply does not close the command terminal. Give the
				// monitor another try before falling back to it again.
				viaShell = env.Stream == nil
			} else {
				mon.close()
				mon = nil
			}
			if fails++; fails >= waitMaxFails {
				return finish(errors.Join(err, fileErr))
			}
			tryFile, fileMisses = useFiles, 1
			logf("The terminal stopped answering (%v); opening another to read on.", err)
			continue
		}
		if !viaFile {
			if viaShell {
				lastShell = "answered"
			} else {
				lastMonitor = "answered"
			}
		}
		misses, fails = 0, 0
		lastConnectionNote = ""
		if a.ready && !viaFile {
			return nil
		}
		if a.from != off {
			// The log shrank, or the plugin stopped with unread logs.
			carry = nil
		}
		carry = showNew(env, carry, a.data, &recent)
		off = a.from + int64(len(a.data))
		if a.ready {
			return nil
		}
		if !a.running {
			if line := pluginLogLine(string(carry)); line != "" {
				recent = append(recent, line)
			}
			if len(recent) == 0 {
				return errors.New("the plugin stopped")
			}
			return fmt.Errorf("the plugin stopped: %s", lastLines(strings.Join(recent, "\n"), 12))
		}
		if off < a.size {
			continue
		}
		select {
		case <-probeCtx.Done():
		case <-time.After(waitPollInterval):
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Running out of time is the plugin's failure, not the terminals'.
	return fmt.Errorf("the plugin didn't get ready in %s", timeout)
}

// snapshotAnswer aligns a file's tail with logs already read by either transport.
func snapshotAnswer(s colab.WaitStatus, nonce string, off int64, now time.Time) (waitAnswer, error) {
	age := now.Sub(time.Unix(s.UpdatedAt, 0))
	if s.Nonce != nonce || age > 30*time.Second || age < -10*time.Second || s.From < 0 ||
		s.Size < s.From || s.Size-s.From != int64(len(s.Log)) || len(s.Log) > waitLogChunk {
		return waitAnswer{}, errors.New("stale or invalid deployment status snapshot")
	}
	a := waitAnswer{size: s.Size, from: s.From, data: s.Log}
	switch s.State {
	case "ready":
		a.ready, a.running = true, true
	case "starting":
		a.running = true
	case "stopped":
	default:
		return waitAnswer{}, errors.New("unknown deployment status snapshot state")
	}
	if off >= s.Size {
		a.from, a.size, a.data = off, off, nil
	} else if off > s.From {
		a.from, a.data = off, s.Log[off-s.From:]
	}
	return a, nil
}

// showNew logs the whole lines of carry and chunk, the bytes the log
// grew by after carry, the start of a line it had, and returns the start
// of a line chunk ends with. A line redrawn with carriage returns, as a
// progress bar, shows as it ended. The phase the lines print goes to
// env.Phase, and the last 12 lines to recent. At most 400 lines show at
// once, the last.
func showNew(env *Env, carry, chunk []byte, recent *[]string) []byte {
	data := append(carry, chunk...)
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return data
	}
	lines := strings.Split(string(data[:end]), "\n")
	// The plugin's script prints its phases.
	if p := lastPhase(string(data[:end])); p != "" {
		env.Phase(p)
	}
	visible := lines[:0]
	for _, line := range lines {
		if line := pluginLogLine(line); line != "" {
			visible = append(visible, line)
		}
	}
	lines = visible
	*recent = append(*recent, lines...)
	if len(*recent) > 12 {
		*recent = append([]string(nil), (*recent)[len(*recent)-12:]...)
	}
	if len(lines) > 400 {
		lines = lines[len(lines)-400:]
	}
	for _, line := range lines {
		env.Log("%s", line)
	}
	return append([]byte(nil), data[end+1:]...)
}

func pluginLogLine(line string) string {
	line = strings.TrimSuffix(line, "\r")
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	line = strings.TrimSpace(stripEscapes(line))
	if strings.HasPrefix(line, "@@PHASE ") && strings.HasSuffix(line, "@@") {
		return ""
	}
	return line
}

func lastPhase(logs string) string {
	i := strings.LastIndex(logs, "@@PHASE ")
	if i < 0 {
		return ""
	}
	rest := logs[i+len("@@PHASE "):]
	if j := strings.Index(rest, "@@"); j >= 0 {
		return rest[:j]
	}
	return ""
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

var escapes = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

// stripEscapes removes terminal escape sequences and other control
// characters from a line of log.
func stripEscapes(s string) string {
	s = escapes.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// ShellQuote quotes s for bash.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
