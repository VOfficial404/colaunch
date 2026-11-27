package core

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/plugins"
	"colaunch/internal/tunnel"

	"golang.org/x/crypto/ssh"
)

// Phases of a session.
const (
	PhaseValidating  = "validating"
	PhaseAssigning   = "assigning"
	PhaseConnecting  = "connecting"
	PhaseRetrying    = "retrying"
	PhaseWaiting     = "waiting"
	PhaseGuarding    = "guarding"
	PhaseDeploying   = "deploying"
	PhaseForwarding  = "forwarding"
	PhaseRunning     = "running"
	PhaseReattaching = "reattaching"
	PhaseStopping    = "stopping"
	PhaseStopped     = "stopped"
	PhaseFailed      = "failed"
)

// Session is a runtime Colaunch assigned, and the plugin on it.
type Session struct {
	sessionRecord

	// What the view shows; changed on the main thread only.
	Phase       string
	PluginPhase string
	Err         string
	Log         []LogLine
	Status      *agent.Status
	Forwards    []*ForwardState
	// StopReason is why it stopped: "user", "schedule", "idle",
	// "budget", "floor", "gone" or "failed".
	StopReason string
	Attempt    int
	// Round is the round of grabbing, NextTry when the next one starts,
	// and TryErr why the last try got nothing.
	Round     int
	NextTry   time.Time
	TryErr    string
	Grabbing  bool
	StoppedAt time.Time
	// Public are the ports reachable from the internet, by remote port;
	// never saved.
	Public map[int]*PublicLink
	// Domains are the ports served on the user's own hostnames, by remote
	// port; never saved.
	Domains map[int]*DomainLink

	// sentWD is the watchdog the runtime was last given, nil for none
	// known; main thread.
	sentWD *agent.Watchdog
	// announceComfy waits for the first verified local ComfyUI forward.
	announceComfy      bool
	rebuildingForwards bool

	// What goroutines use, behind mu.
	mu sync.Mutex
	// rid is RuntimeID for goroutines: the main thread's may lag behind.
	rid    string
	conn   *colab.Connection
	shell  colab.Transport
	mux    *tunnel.Mux
	fwds   []*tunnel.Forward
	cancel context.CancelFunc
	// dialing is the tunnel being opened, which others wait for; gen
	// counts closeConns, so that a tunnel opened across one is dropped.
	dialing       *muxDial
	gen           int
	stopping      bool       // normal operations may not start during release
	busy          sync.Mutex // one watch at a time
	transportGate chan struct{}
	transportID   string
	hostKey       ssh.PublicKey
	ttyStatus     int
	muxStarted    bool
	lastTraffic   time.Time
	initialized   bool
}

// muxDial is a tunnel being opened.
type muxDial struct {
	done   chan struct{}
	cancel context.CancelFunc
	mux    *tunnel.Mux
	err    error
}

// muxIdle is how long a tunnel without connections stays open; the next
// connection opens it again.
const muxIdle = 5 * time.Minute

// runtimeID is the session's runtime, for goroutines.
func (s *Session) runtimeID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rid
}

// LogLine is a line of a session's log.
type LogLine struct {
	Time time.Time
	Text string
}

func (s *Session) init() { s.rid = s.RuntimeID }

// Ended reports whether the session's runtime is gone.
func (s *Session) Ended() bool {
	return s.Phase == PhaseStopped || s.Phase == PhaseFailed
}

// Busy reports whether the session is on its way up.
func (s *Session) Busy() bool {
	switch s.Phase {
	case PhaseRunning, PhaseStopped, PhaseFailed, PhaseStopping:
		return false
	}
	return true
}

// Uptime is how long the runtime has been assigned.
func (s *Session) Uptime() time.Duration {
	end := time.Now()
	if s.Ended() && !s.StoppedAt.IsZero() {
		end = s.StoppedAt
	}
	return end.Sub(s.Created)
}

// OpenAI returns the base URL of the session's OpenAI-compatible API on
// this machine, "" for none.
func (s *Session) OpenAI() string {
	for _, f := range s.Forwards {
		if f.Port.Kind == plugins.PortOpenAI && f.f != nil {
			return "http://" + f.f.Local + "/v1"
		}
	}
	return ""
}

// StopAt is when the session's schedule stops it, zero for never.
func (s *Session) StopAt() time.Time {
	if s.Guards.StopAfter <= 0 {
		return time.Time{}
	}
	return s.Created.Add(time.Duration(s.Guards.StopAfter * float64(time.Minute)))
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// logBatch is how long log lines wait to be shown together, one redraw
// for all.
const logBatch = 100 * time.Millisecond

// workState is what the manager's goroutines share.
type workState struct {
	mu   sync.Mutex
	logs []pendingLog // behind mu
	// background is true while the app is in the background; wake tells
	// the loop it changed.
	background atomic.Bool
	wakeOnce   sync.Once
	wake       chan struct{}
}

type pendingLog struct {
	s    *Session
	line LogLine
}

func (w *workState) wakeCh() chan struct{} {
	w.wakeOnce.Do(func() { w.wake = make(chan struct{}, 1) })
	return w.wake
}

func (m *Manager) logf(s *Session, format string, args ...any) {
	line := LogLine{Time: time.Now(), Text: fmt.Sprintf(format, args...)}
	w := &m.work
	w.mu.Lock()
	w.logs = append(w.logs, pendingLog{s, line})
	first := len(w.logs) == 1
	w.mu.Unlock()
	if first {
		time.AfterFunc(logBatch, m.flushLogs)
	}
}

// flushLogs shows the log lines waiting, with those that came while its
// Post waited.
func (m *Manager) flushLogs() {
	m.Post(func() {
		w := &m.work
		w.mu.Lock()
		logs := w.logs
		w.logs = nil
		w.mu.Unlock()
		for _, l := range logs {
			s := l.s
			s.Log = append(s.Log, l.line)
			if len(s.Log) > 2000 {
				s.Log = s.Log[len(s.Log)-1500:]
			}
		}
	})
}

func (m *Manager) phase(s *Session, gen int, p string) {
	m.Post(func() {
		if !s.currentGeneration(gen) || s.Ended() {
			return
		}
		s.Phase = p
		if p != PhaseDeploying {
			s.PluginPhase = ""
		}
	})
}

// Launch assigns a runtime as the form says and runs its plugin there.
func (m *Manager) Launch(form DeployForm) (*Session, error) {
	a := m.Active()
	if a == nil {
		return nil, errors.New("no account")
	}
	p := plugins.Get(form.Plugin)
	if p == nil {
		return nil, fmt.Errorf("no plugin %q", form.Plugin)
	}
	if WebTask(p) && a.FreeTier() && !m.Settings.Risky.FreeWebUI {
		return nil, ErrFreeWebUI
	}
	machine := m.grabMachine(form.Machine)
	values := map[string]string{}
	for k, v := range plugins.Defaults(p) {
		values[k] = v
	}
	for k, v := range form.Values[form.Plugin] {
		values[k] = v
	}
	s := &Session{
		sessionRecord: sessionRecord{
			ID:      newID(),
			Account: a.Email,
			Spec:    form.Machine.Spec(),
			Plugin:  p.ID(),
			Values:  values,
			Guards:  form.Guards,
			Created: time.Now(),
		},
		Phase: PhaseValidating,
	}
	if a.Quota != nil {
		s.StartBalance = a.Quota.Balance
	}
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	m.Sessions = append(m.Sessions, s)
	s.Grabbing = machine.Grab
	// The active account only: Colab's terms forbid using several to get
	// around its limits; there is no option to.
	go m.launch(ctx, s, a, p, machine, m.Settings.MaxAttempts, s.gen)
	return s, nil
}

func (m *Manager) fail(s *Session, a *Account, err error) {
	gen, live := s.liveGeneration()
	if !live || errors.Is(err, context.Canceled) || errors.Is(err, errReplaced) {
		return
	}
	m.logf(s, "✗ %s", errText(err))
	id := s.runtimeID()
	keep := m.Settings.KeepFailed && id != ""
	if id != "" && !keep {
		m.logf(s, "Giving the runtime back, so that it costs nothing more.")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if derr := a.Client.DeleteRuntime(ctx, id); derr != nil {
			m.logf(s, "✗ %s", errText(derr))
		}
		cancel()
	}
	if !s.closeGeneration(gen) {
		return
	}
	m.Post(func() {
		if !s.currentGeneration(gen+1) || s.Ended() || s.Phase == PhaseStopping {
			return
		}
		s.Err = errText(err)
		m.alertFailed(s, err)
		if keep {
			s.Phase = PhaseRunning
			m.Notify("error", s.Title(), s.Err)
			return
		}
		s.Phase = PhaseFailed
		s.StopReason = "failed"
		s.StoppedAt = time.Now()
		m.saveState()
		m.Notify("error", s.Title(), s.Err)
	})
}

// Title is how the session shows.
func (s *Session) Title() string {
	name := s.Plugin
	if p := plugins.Get(s.Plugin); p != nil {
		name = p.Name().In("en")
	}
	if s.Result != nil && s.Result.Model != "" {
		name = s.Result.Model
	} else if r := s.Values["repo"]; r != "" {
		name = r
	}
	return name
}

func (m *Manager) launch(ctx context.Context, s *Session, a *Account, p plugins.Plugin, machine Machine, vramTries, gen int) {
	m.logf(s, "%s · %s · %s", p.Name().In(m.Lang()), s.Spec, a.Email)
	hw, err := p.Validate(ctx, s.Values, m.HFToken())
	if err != nil {
		m.fail(s, a, err)
		return
	}
	if hw != nil && hw.MinMemory > 0 {
		m.logf(s, "The plugin needs ≈ %.1f GB of memory at the least.", float64(hw.MinMemory)/1e9)
	}

	minVRAM := int64(machine.MinVRAM * 1024) // MiB
	if s.Spec.Variant != colab.GPU {
		minVRAM = 0
	}
	var sh colab.Transport
	var st *agent.Status
	try := func(spec colab.Spec) error {
		rt, err := a.Client.CreateRuntime(ctx, spec)
		if err != nil {
			return err
		}
		id := rt.ID()
		release := func() {
			dctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := a.Client.DeleteRuntime(dctx, id); err != nil {
				m.logf(s, "✗ %s", errText(err))
			}
			s.mu.Lock()
			if s.gen == gen && s.rid == id {
				s.rid, s.conn = "", nil
			}
			s.mu.Unlock()
			m.Post(func() {
				if s.currentGeneration(gen) && s.RuntimeID == id {
					s.RuntimeID = ""
					m.saveState()
				}
			})
		}
		s.mu.Lock()
		if s.gen != gen || ctx.Err() != nil {
			s.mu.Unlock()
			release()
			return context.Canceled
		}
		s.rid, s.conn = id, rt.Connection
		s.mu.Unlock()
		m.Post(func() {
			if s.currentGeneration(gen) {
				s.RuntimeID = id
				m.saveState()
			}
		})
		m.logf(s, "Assigned runtime %s.", id)
		m.phase(s, gen, PhaseConnecting)
		sh2, st2, err := m.connect(ctx, s, a, id, rt.Connection, gen)
		if ctx.Err() != nil || !s.currentGeneration(gen) {
			return context.Canceled // Stop owns the assigned runtime.
		}
		if err != nil {
			release()
			var upgrade *colab.SSHError
			if proxyUnauthorized(err) || (errors.As(err, &upgrade) && upgrade.Status == http.StatusTooManyRequests) {
				return err
			}
			return fmt.Errorf("%w: %s", errConnect, errText(err))
		}
		vram := st2.VRAMMB()
		gpuName := ""
		if len(st2.GPUs) > 0 {
			gpuName = st2.GPUs[0].Name
		}
		m.logf(s, "Machine: %s, %d CPUs, RAM %s, disk free %s.", orNone(gpuName, fmtGB(vram)), st2.CPUs, fmtGB(st2.Memory.Total>>20), fmtGB(st2.Disk.Free>>20))
		if minVRAM > 0 && vram < minVRAM {
			sh2.Close()
			release()
			return fmt.Errorf("%w: got %s, want %s", errTooLittleVRAM, fmtGB(vram), fmtGB(minVRAM))
		}
		m.Post(func() {
			if s.currentGeneration(gen) {
				s.VRAM, s.GPUName, s.Status = vram, gpuName, st2
				m.saveState()
			}
		})
		sh, st = sh2, st2
		return nil
	}
	got, err := m.grab(ctx, s, machine, vramTries, try)
	if err != nil {
		m.fail(s, a, err)
		return
	}
	m.Post(func() {
		if a.Quota != nil {
			s.StartBalance = a.Quota.Balance
		}
		m.saveState()
	})
	if got.tries > 1 {
		m.logf(s, "Got %s after %d tries in %s.", got.spec, got.tries, got.waited.Round(time.Second))
		title, text := s.Title(), fmt.Sprintf("%s · %d × · %s", got.spec, got.tries, got.waited.Round(time.Second))
		m.Post(func() { m.Notify("grabbed", title, text) })
	}
	if machine.Grab || got.tries > 1 {
		m.Post(func() { m.alertGrabbed(s, got) })
	}

	m.phase(s, gen, PhaseGuarding)
	if err := m.applyWatchdog(ctx, s, a, sh); err != nil {
		m.fail(s, a, err)
		return
	}

	m.phase(s, gen, PhaseDeploying)
	settings := map[string]string{"llama_cuda_url": m.Settings.LlamaCUDAURL}
	env := &plugins.Env{
		Values:   s.Values,
		Shell:    sh,
		Spec:     got.spec,
		Status:   st,
		Lang:     m.Lang(),
		HFToken:  m.HFToken(),
		Settings: settings,
		Log:      func(f string, args ...any) { m.logf(s, f, args...) },
		Phase: func(p string) {
			m.Post(func() {
				if s.currentGeneration(gen) {
					s.PluginPhase = p
				}
			})
		},
		Control: func(ctx context.Context) (colab.Transport, error) {
			return m.shellOf(ctx, s, a)
		},
		ReadWaitStatus: func(ctx context.Context, plugin, nonce string) (colab.WaitStatus, error) {
			return runtimeWaitStatus(ctx, s, a, plugin, nonce)
		},
	}
	if _, ok := sh.(*colab.SSH); ok {
		env.Stream = func(ctx context.Context, command, ready string) (plugins.StreamConn, *bufio.Reader, error) {
			return m.stream(ctx, s, a, command, ready)
		}
	}
	res, err := p.Deploy(ctx, env)
	if err != nil {
		m.failDeploy(ctx, s, a, err)
		return
	}
	comfy := false
	for _, p := range res.Ports {
		comfy = comfy || p.Kind == plugins.PortComfy
	}
	m.Post(func() {
		if s.currentGeneration(gen) {
			s.Result, s.announceComfy = res, comfy
			m.saveState()
		}
	})

	m.phase(s, gen, PhaseForwarding)
	m.forwardAll(s, a, gen, res.Ports, nil)
	m.phase(s, gen, PhaseRunning)
	if !comfy {
		m.Post(func() {
			if s.currentGeneration(gen) {
				m.logf(s, "✓ Ready.")
				m.Notify("success", s.Title(), s.OpenAI())
				m.alertReady(s)
			}
		})
	}
}

func orNone(name, mem string) string {
	if name == "" {
		return "no GPU"
	}
	return name + " " + mem
}

// watchdogFor computes the runtime's limits: the schedule, and the time
// the balance allows at the account's rate.
func watchdogFor(s *sessionRecord, q *colab.Quota, now time.Time) agent.Watchdog {
	w := agent.Watchdog{IdleMinutes: s.Guards.IdleStop}
	if s.Guards.StopAfter > 0 {
		w.StopAt = s.Created.Add(time.Duration(s.Guards.StopAfter * float64(time.Minute))).Unix()
	}
	if q != nil && q.RateHourly > 0 {
		hours := math.Inf(1)
		if s.Guards.SpendCap > 0 && s.StartBalance > 0 {
			left := s.Guards.SpendCap - (s.StartBalance - q.Balance)
			hours = math.Min(hours, left/q.RateHourly)
		}
		if s.Guards.BalanceFloor > 0 {
			hours = math.Min(hours, (q.Balance-s.Guards.BalanceFloor)/q.RateHourly)
		}
		if !math.IsInf(hours, 1) {
			hours = math.Max(hours, 0)
			w.BudgetAt = now.Add(time.Duration(hours * float64(time.Hour))).Unix()
		}
	}
	return w
}

func (m *Manager) applyWatchdog(ctx context.Context, s *Session, a *Account, sh colab.Transport) error {
	var w agent.Watchdog
	var guarded bool
	done := make(chan struct{})
	m.peek(func() {
		w = watchdogFor(&s.sessionRecord, a.Quota, time.Now())
		guarded = s.Guards.Any()
		close(done)
	})
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := agent.SetWatchdog(ctx, sh, w); err != nil {
		m.peek(func() { s.sentWD = nil })
		return err
	}
	m.peek(func() { s.sentWD = &w })
	m.applyNotices(ctx, s, sh)
	if !guarded {
		return nil
	}
	var parts []string
	if w.StopAt > 0 {
		parts = append(parts, "stop at "+time.Unix(w.StopAt, 0).Format("15:04"))
	}
	if w.IdleMinutes > 0 {
		parts = append(parts, fmt.Sprintf("idle %.0f min", w.IdleMinutes))
	}
	if w.BudgetAt > 0 {
		parts = append(parts, "budget until "+time.Unix(w.BudgetAt, 0).Format("01-02 15:04"))
	}
	m.logf(s, "Watchdog on the runtime: %s. It holds with the app closed.", strings.Join(parts, ", "))
	return nil
}

// checkBalance stops the account's sessions whose spending limits it
// passed. It runs on the main thread, after a quota came in.
func (m *Manager) checkBalance(a *Account) {
	q := a.Quota
	for _, s := range m.Sessions {
		if s.Account != a.Email || s.Phase != PhaseRunning {
			continue
		}
		if s.Guards.SpendCap > 0 && s.StartBalance > 0 && s.StartBalance-q.Balance >= s.Guards.SpendCap {
			m.Stop(s, "budget")
			continue
		}
		if s.Guards.BalanceFloor > 0 && q.Balance <= s.Guards.BalanceFloor && (q.Balance > 0 || s.StartBalance > 0) {
			m.Stop(s, "floor")
			continue
		}
		if s.Guards.SpendCap > 0 || s.Guards.BalanceFloor > 0 {
			// The rate may have changed: tell the watchdog, unless all
			// that moved is the budget's end, by less than a minute.
			w := watchdogFor(&s.sessionRecord, q, time.Now())
			if sameWatchdog(s.sentWD, w) {
				continue
			}
			s.sentWD = &w
			go func() {
				ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
				defer cancel()
				sh, err := m.shellOf(ctx, s, a)
				if err != nil {
					m.peek(func() { s.sentWD = nil })
					return
				}
				if agent.SetWatchdog(ctx, sh, w) != nil {
					m.peek(func() { s.sentWD = nil })
				}
			}()
		}
	}
}

// sameWatchdog reports whether a runtime given old needs w no more: they
// are the same but for a budget's end less than a minute apart.
func sameWatchdog(old *agent.Watchdog, w agent.Watchdog) bool {
	if old == nil || old.StopAt != w.StopAt || old.IdleMinutes != w.IdleMinutes {
		return false
	}
	if (old.BudgetAt == 0) != (w.BudgetAt == 0) {
		return false
	}
	d := old.BudgetAt - w.BudgetAt
	return d > -60 && d < 60
}

// Stop gives a session's runtime back.
func (m *Manager) Stop(s *Session, reason string) {
	if s.Ended() || s.Phase == PhaseStopping {
		return
	}
	a := m.Account(s.Account)
	s.Phase = PhaseStopping
	s.StopReason = reason
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.stopping = true
	s.gen++
	gen, id := s.gen, s.rid
	fwds, mux, dialing := s.fwds, s.mux, s.dialing
	s.fwds = nil
	s.mu.Unlock()
	s.Forwards, s.rebuildingForwards = nil, false
	shares := s.dropPublicLinks()
	domains := s.dropDomainLinks()
	go func() {
		if dialing != nil {
			dialing.cancel()
		}
		if mux != nil {
			mux.Close()
		}
		for _, f := range fwds {
			f.Close()
		}
		if a != nil && len(shares)+len(domains) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if sh, err := m.shellOfGeneration(ctx, s, a, gen); err == nil {
				for _, port := range shares {
					stopShare(ctx, sh, port)
				}
				for _, config := range domains {
					stopDomain(ctx, sh, config)
				}
			}
			cancel()
		}
		s.closeGeneration(gen)
		var err error
		if id != "" && a != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			err = a.Client.DeleteRuntime(ctx, id)
			cancel()
		}
		m.Post(func() {
			if err != nil {
				s.mu.Lock()
				s.stopping = false
				gen := s.gen
				s.mu.Unlock()
				s.RuntimeID = id
				s.Phase, s.StopReason = PhaseRunning, ""
				s.Err = errText(err)
				m.saveState()
				m.Notify("error", s.Title(), s.Err)
				m.restoreForwards(s, a, gen)
				return
			}
			s.Phase = PhaseStopped
			s.StoppedAt = time.Now()
			m.saveState()
			m.Notify("info", s.Title(), "")
			// A session stopped before it had a runtime stopped nothing
			// that costs.
			if id != "" {
				m.alertStopped(s, false)
			}
		})
		if a != nil {
			m.refreshAccount(a)
		}
	}()
}

// closeConns closes the session's terminals and forwards, and stops the
// tunnel being opened, if one is. What may wait on the network is done
// without mu.
func (s *Session) closeConns() {
	s.closeGeneration(-1)
}

// closeGeneration takes the specified generation out of service. A negative
// generation closes whichever resources are current, as on app shutdown.
func (s *Session) closeGeneration(gen int) bool {
	s.mu.Lock()
	if gen >= 0 && s.gen != gen {
		s.mu.Unlock()
		return false
	}
	fwds, mux, sh, d := s.fwds, s.mux, s.shell, s.dialing
	s.fwds, s.mux, s.shell, s.dialing = nil, nil, nil, nil
	s.gen++
	s.mu.Unlock()
	if d != nil {
		d.cancel()
	}
	// The tunnel first: its streams then close without a word to it.
	if mux != nil {
		mux.Close()
	}
	for _, f := range fwds {
		f.Close()
	}
	if sh != nil {
		sh.Close()
	}
	return true
}

// dialMux returns the session's tunnel, opening it if it isn't open. It
// doesn't hold mu while it dials, and callers at the same time share one
// dial.
func (s *Session) dialMux(ctx context.Context, gen int) (*tunnel.Mux, error) {
	s.mu.Lock()
	if s.gen != gen || ctx.Err() != nil {
		s.mu.Unlock()
		return nil, errReplaced
	}
	var closedMux *tunnel.Mux
	if s.mux != nil {
		select {
		case <-s.mux.Done():
			closedMux, s.mux = s.mux, nil
		default:
			m := s.mux
			s.mu.Unlock()
			return m, nil
		}
	}
	if d := s.dialing; d != nil {
		s.mu.Unlock()
		select {
		case <-d.done:
			return d.mux, d.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	conn := s.conn
	if conn == nil {
		s.mu.Unlock()
		return nil, errors.New("not connected")
	}
	ctx, cancel := context.WithCancel(ctx)
	d := &muxDial{done: make(chan struct{}), cancel: cancel}
	s.dialing = d
	s.muxStarted = true
	s.mu.Unlock()

	if closedMux != nil {
		closedMux.Close()
	}
	tty, r, err := streamMux(ctx, conn)
	cancel()
	var mux *tunnel.Mux
	if err == nil {
		mux = tunnel.New(tty, r)
		mux.SetIdleTimeout(muxIdle)
	}
	s.mu.Lock()
	if s.dialing == d {
		s.dialing = nil
	}
	stale := s.gen != gen
	if mux != nil && !stale {
		s.mux = mux
	}
	s.mu.Unlock()
	if mux != nil && stale {
		// The session closed while it dialed.
		mux.Close()
		mux, err = nil, tunnel.ErrClosed
	}
	d.mux, d.err = mux, err
	close(d.done)
	return mux, err
}

// streamMux starts the agent's mux on a terminal; tests replace it.
var streamMux = func(ctx context.Context, conn *colab.Connection) (tunnel.Transport, *bufio.Reader, error) {
	tty, r, err := colab.StreamMux(ctx, conn, agent.MuxCommand, agent.MuxReady)
	if err != nil {
		return nil, nil, err
	}
	return tty, r, nil
}

func runtimeConnection(ctx context.Context, client *colab.Client, id string) (*colab.Connection, error) {
	rt, err := client.Runtime(ctx, id)
	if err != nil {
		return nil, err
	}
	if rt.Connection == nil {
		return nil, errors.New("the runtime has no connection")
	}
	return rt.Connection, nil
}

// watch looks at a running session's runtime, of account a: is it still
// there, what does it use, did its watchdog stop it.
func (m *Manager) watch(s *Session, a *Account) {
	if !s.busy.TryLock() {
		return
	}
	defer s.busy.Unlock()
	gen, live := s.liveGeneration()
	if !live {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 90*time.Second)
	defer cancel()
	sh, err := m.shellOfGeneration(ctx, s, a, gen)
	if errors.Is(err, colab.ErrNotFound) {
		m.gone(s, gen)
		return
	}
	if err != nil {
		return
	}
	st, err := agent.GetStatus(ctx, sh)
	if err != nil {
		return
	}
	m.Post(func() {
		if !s.currentGeneration(gen) || s.Phase != PhaseRunning {
			return
		}
		s.Status, s.Err = st, ""
		m.restoreForwards(s, a, gen)
	})
}

// gone marks a session whose runtime no longer exists.
func (m *Manager) gone(s *Session, gen int) {
	m.Post(func() {
		if s.Ended() || s.Phase == PhaseStopping {
			return
		}
		s.mu.Lock()
		if s.gen != gen {
			s.mu.Unlock()
			return
		}
		s.stopping = true
		s.gen++
		closingGen := s.gen
		s.mu.Unlock()
		s.Phase = PhaseStopped
		go s.closeGeneration(closingGen)
		s.StoppedAt = time.Now()
		s.StopReason = "gone"
		s.dropPublicLinks()
		s.dropDomainLinks()
		byRuntime := false
		if st := s.Status; st != nil && st.Stopped != nil {
			s.StopReason = st.Stopped.Reason
			byRuntime = watchdogReasons[s.StopReason]
		}
		s.Forwards = nil
		m.saveState()
		m.Notify("info", s.Title(), s.StopReason)
		m.alertStopped(s, byRuntime)
	})
}

// reattach finds a kept session's runtime after the app restarted, and
// forwards its ports again.
func (m *Manager) reattach(s *Session, a *Account) {
	gen, live := s.liveGeneration()
	if !live {
		return
	}
	id := s.runtimeID()
	if a == nil || id == "" {
		m.gone(s, gen)
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()
	conn, err := runtimeConnection(ctx, a.Client, id)
	if errors.Is(err, colab.ErrNotFound) {
		m.gone(s, gen)
		return
	}
	if err != nil {
		m.Post(func() {
			if s.currentGeneration(gen) && s.Phase == PhaseReattaching {
				s.Phase, s.Err = PhaseRunning, errText(err)
			}
		})
		return
	}
	sh, st, err := m.connect(ctx, s, a, id, conn, gen)
	if err != nil {
		m.Post(func() {
			if s.currentGeneration(gen) && s.Phase == PhaseReattaching {
				s.Phase, s.Err = PhaseRunning, errText(err)
			}
		})
		return
	}
	// Public links and domains aren't kept: close those the app had
	// before it restarted, which it no longer shows.
	for _, id := range leftShares(st) {
		if err := agent.StopPlugin(ctx, sh, id); err == nil {
			m.logf(s, "Closed a public link left from before the restart (%s).", id)
		}
	}
	m.Post(func() {
		if !s.currentGeneration(gen) || s.Phase != PhaseReattaching {
			return
		}
		s.Status, s.Err, s.Phase = st, "", PhaseRunning
		m.restoreForwards(s, a, gen)
		m.logf(s, "Reconnected to runtime %s.", id)
	})
}

// UpdateGuards changes a running session's limits, on the runtime too.
func (m *Manager) UpdateGuards(s *Session, g Guards) {
	s.Guards = g
	m.saveState()
	a := m.Account(s.Account)
	if a == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		sh, err := m.shellOf(ctx, s, a)
		if err == nil {
			err = m.applyWatchdog(ctx, s, a, sh)
		}
		if err != nil {
			m.Post(func() { m.Notify("error", s.Title(), errText(err)) })
		}
	}()
}
