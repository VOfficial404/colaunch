// Package plugins is what Colaunch runs on a runtime once it's assigned.
// Deploying a model is a plugin like any other: built in, written in Go.
// Users add their own as a folder with a plugin.json and a script (see
// docs/plugins.md and ScriptPlugin).
package plugins

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
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
	Shell   *colab.Shell
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
	// Reconnect, if set, opens another terminal on the same runtime, in
	// place of Shell, which it closes, for when Shell stopped answering.
	// What runs on the runtime is left be. Waiting for a plugin to get
	// ready uses it, and sets Shell to what it returns.
	Reconnect func(ctx context.Context) (*colab.Shell, error)
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
	Stop(ctx context.Context, sh *colab.Shell) error
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
)

// waitProbeTimeout bounds one probe: a terminal that doesn't answer in it
// is given up for another.
var waitProbeTimeout = 2 * time.Minute

// waitReadyProbe builds the command that runs the check. If the check fails,
// it reports liveness and at most one chunk of log bytes, in base64 to
// preserve carriage returns. Once stopped, read the end for diagnostics.
// Its markers carry token, new for each probe, so that its answer can be
// told from any other's.
func waitReadyProbe(id, token string, off int64, check string) string {
	// Keep the built-in probes short enough for Shell.Run to send inline.
	return fmt.Sprintf(`if (%s) >/dev/null 2>&1 </dev/null; then
  echo "@@WAIT-READY %s@@"
else
  python3 %s wait-log %s %s %d %d
fi`, check, token, ShellQuote(agent.Path), ShellQuote(id), ShellQuote(token), off, waitLogChunk)
}

// newProbeToken is a probe's token; tests replace it.
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

// parseWaitAnswer reads the answer, with token, of the probe of the log
// from off. Exactly one answer must be in out, and end it, whole: its
// header well formed and the bytes as many as it says. What comes before
// the answer is allowed, for output whose start marker the terminal lost.
func parseWaitAnswer(out, token string, off int64) (waitAnswer, bool) {
	ready := "@@WAIT-READY " + token + "@@\n"
	head := "@@WAIT " + token + " "
	nr, nh := strings.Count(out, ready), strings.Count(out, head)
	if nr == 1 && nh == 0 && strings.HasSuffix(out, ready) {
		return waitAnswer{ready: true}, true
	}
	if nr != 0 || nh != 1 {
		return waitAnswer{}, false
	}
	line, data, ok := strings.Cut(out[strings.Index(out, head)+len(head):], "\n")
	enc, last := strings.CutSuffix(data, "\n")
	line, closed := strings.CutSuffix(line, "@@")
	f := strings.Split(line, " ")
	if !ok || !last || !closed || len(f) != 3 || strings.Contains(enc, "\n") {
		return waitAnswer{}, false
	}
	size, err1 := strconv.ParseInt(f[0], 10, 64)
	from, err2 := strconv.ParseInt(f[1], 10, 64)
	run := f[2]
	if err1 != nil || err2 != nil || (run != "yes" && run != "no" && run != "unknown") {
		return waitAnswer{}, false
	}
	if size < 0 {
		return waitAnswer{}, false
	}
	// Mirror the probe's range: start at off (or zero after a shrink),
	// and take the last unread chunk if the plugin has stopped.
	wantFrom := off
	if off > size {
		wantFrom = 0
	}
	n := min(size-wantFrom, waitLogChunk)
	if run == "no" {
		wantFrom = size - n
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || from != wantFrom || int64(len(b)) != n {
		return waitAnswer{}, false
	}
	return waitAnswer{size: size, from: from, running: run != "no", data: b}, true
}

// runWaitProbe reports whether END was consumed, so an unusable answer can
// be retried on the same shell when the next command can still run there.
func runWaitProbe(ctx context.Context, sh *colab.Shell, id string, off int64, check string) (waitAnswer, bool, error) {
	token := newProbeToken()
	runCtx, cancel := context.WithTimeout(ctx, waitProbeTimeout)
	defer cancel()
	out, status, err := sh.Run(runCtx, waitReadyProbe(id, token, off, check))
	if before, endStatus, ok := colab.MissingStartOutput(err); ok {
		out, status = before, endStatus
	} else if err != nil {
		return waitAnswer{}, false, err
	}
	if status != 0 {
		statusErr := fmt.Errorf("readiness probe exited with status %d", status)
		if err != nil {
			statusErr = fmt.Errorf("%w: %v", err, statusErr)
		}
		return waitAnswer{}, true, statusErr
	}
	if a, ok := parseWaitAnswer(out, token, off); ok {
		return a, true, nil
	}
	if err == nil {
		err = errors.New("invalid readiness probe response")
	}
	return waitAnswer{}, true, err
}

// waitReady polls check every few seconds until it's true, the plugin's
// process ends, or ctx is done. Each poll shows the log's new lines.
//
// If END arrives but the probe fails or its answer is incomplete, retry
// from the last confirmed log offset on the same terminal first. A closed
// or unresponsive terminal is replaced immediately if Reconnect is set.
func waitReady(ctx context.Context, env *Env, id string, timeout time.Duration, check string) error {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxMisses := 3
	if env.Reconnect != nil {
		maxMisses = 2
	}
	const maxReconnects = 3
	var misses, reconnects int
	var off int64
	var carry []byte
	var recent []string
	logf := func(format string, args ...any) {
		if env.Log != nil {
			env.Log(format, args...)
		}
	}
probe:
	for probeCtx.Err() == nil {
		a, hasEnd, err := runWaitProbe(probeCtx, env.Shell, id, off, check)
		if probeCtx.Err() != nil {
			break
		}
		if err != nil {
			if hasEnd {
				misses++
				if misses < maxMisses {
					continue
				}
			}
			for env.Reconnect != nil && reconnects < maxReconnects {
				if reconnects > 0 {
					select {
					case <-probeCtx.Done():
						break probe
					case <-time.After(3 * time.Second):
					}
				}
				if probeCtx.Err() != nil {
					break probe
				}
				reconnects++
				logf("The terminal stopped answering (%v); opening another to read on.", err)
				reconnectCtx, reconnectCancel := context.WithTimeout(probeCtx, waitReconnectTimeout)
				sh, rerr := env.Reconnect(reconnectCtx)
				reconnectCancel()
				if probeCtx.Err() != nil {
					break probe
				}
				if rerr == nil {
					env.Shell, misses = sh, 0
					continue probe
				}
				logf("✗ %v", rerr)
				if reconnects == maxReconnects {
					return fmt.Errorf("%w (opening another terminal: %v)", err, rerr)
				}
			}
			return err
		}
		misses, reconnects = 0, 0
		if a.ready {
			return nil
		}
		if a.from != off {
			// The log shrank, or the plugin stopped with unread logs.
			carry = nil
		}
		carry = showNew(env, carry, a.data, &recent)
		off = a.from + int64(len(a.data))
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
		case <-time.After(4 * time.Second):
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("the plugin didn't get ready in %s", timeout)
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
