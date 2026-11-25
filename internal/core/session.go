package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/plugins"
	"colaunch/internal/tunnel"
)

// Phases of a session.
const (
	PhaseValidating  = "validating"
	PhaseAssigning   = "assigning"
	PhaseConnecting  = "connecting"
	PhaseRetrying    = "retrying"
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
	Resources   *colab.Resources
	Forwards    []*ForwardState
	// StopReason is why it stopped: "user", "schedule", "idle",
	// "budget", "floor", "gone" or "failed".
	StopReason string
	Attempt    int
	StoppedAt  time.Time

	// What goroutines use, behind mu.
	mu     sync.Mutex
	conn   *colab.Connection
	shell  *colab.Shell
	mux    *tunnel.Mux
	fwds   []*tunnel.Forward
	cancel context.CancelFunc
	busy   sync.Mutex // one watch at a time
}

// LogLine is a line of a session's log.
type LogLine struct {
	Time time.Time
	Text string
}

// ForwardState is a port forwarded to 127.0.0.1.
type ForwardState struct {
	Port plugins.Port
	f    *tunnel.Forward
	Err  string
	// Extra is a forward the user added.
	Extra bool
}

// Address is where the forward listens, "" when it doesn't.
func (f *ForwardState) Address() string {
	if f.f == nil {
		return ""
	}
	return f.f.Local
}

// Conns is how many connections are open through it.
func (f *ForwardState) Conns() int {
	if f.f == nil {
		return 0
	}
	return f.f.Conns()
}

// URL is the forward's address as a link.
func (f *ForwardState) URL() string {
	if f.f == nil {
		return ""
	}
	return "http://" + f.f.Local + f.Port.Path
}

func (s *Session) init() {}

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

func (m *Manager) logf(s *Session, format string, args ...any) {
	line := LogLine{Time: time.Now(), Text: fmt.Sprintf(format, args...)}
	m.Post(func() {
		s.Log = append(s.Log, line)
		if len(s.Log) > 2000 {
			s.Log = s.Log[len(s.Log)-1500:]
		}
	})
}

func (m *Manager) phase(s *Session, p string) {
	m.Post(func() {
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
	go m.launch(ctx, s, a, p, form.Machine)
	return s, nil
}

func (m *Manager) fail(s *Session, a *Account, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	m.logf(s, "✗ %s", errText(err))
	s.mu.Lock()
	id := s.RuntimeID
	s.mu.Unlock()
	keep := m.Settings.KeepFailed && id != ""
	if id != "" && !keep {
		m.logf(s, "Giving the runtime back, so that it costs nothing more.")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if derr := a.Client.DeleteRuntime(ctx, id); derr != nil {
			m.logf(s, "✗ %s", errText(derr))
		}
		cancel()
	}
	s.closeConns()
	m.Post(func() {
		s.Err = errText(err)
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

func (m *Manager) launch(ctx context.Context, s *Session, a *Account, p plugins.Plugin, machine Machine) {
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
	attempts := m.Settings.MaxAttempts
	if minVRAM <= 0 || s.Spec.Variant != colab.GPU {
		attempts = 1
	}
	var sh *colab.Shell
	var st *agent.Status
	for attempt := 1; ; attempt++ {
		m.Post(func() { s.Attempt = attempt })
		m.phase(s, PhaseAssigning)
		m.logf(s, "Assigning %s (%d/%d)…", s.Spec, attempt, attempts)
		rt, err := a.Client.CreateRuntime(ctx, s.Spec)
		if err != nil {
			if errors.Is(err, colab.ErrTooManyRuntimes) {
				err = fmt.Errorf("%w: stop one of its runtimes first", err)
			}
			m.fail(s, a, err)
			return
		}
		s.mu.Lock()
		s.RuntimeID = rt.ID()
		s.conn = rt.Connection
		s.mu.Unlock()
		m.Post(func() { s.RuntimeID = rt.ID(); m.saveState() })
		m.logf(s, "Assigned runtime %s.", rt.ID())

		m.phase(s, PhaseConnecting)
		sh, st, err = m.connect(ctx, s, rt.Connection)
		if err != nil {
			m.fail(s, a, err)
			return
		}
		vram := st.VRAMMB()
		gpuName := ""
		if len(st.GPUs) > 0 {
			gpuName = st.GPUs[0].Name
		}
		m.logf(s, "Machine: %s, %d CPUs, RAM %s, disk free %s.", orNone(gpuName, fmtGB(vram)), st.CPUs, fmtGB(st.Memory.Total>>20), fmtGB(st.Disk.Free>>20))
		if minVRAM > 0 && vram < minVRAM && s.Spec.Variant == colab.GPU {
			sh.Close()
			if attempt >= attempts {
				m.fail(s, a, fmt.Errorf("got %s of GPU memory %d times, not %s: try again later", fmtGB(vram), attempts, fmtGB(minVRAM)))
				return
			}
			m.phase(s, PhaseRetrying)
			m.logf(s, "%s < %s: giving it back and trying again.", fmtGB(vram), fmtGB(minVRAM))
			if err := a.Client.DeleteRuntime(ctx, rt.ID()); err != nil {
				m.fail(s, a, err)
				return
			}
			s.mu.Lock()
			s.RuntimeID = ""
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		m.Post(func() {
			s.VRAM, s.GPUName, s.Status = vram, gpuName, st
			m.saveState()
		})
		break
	}

	m.phase(s, PhaseGuarding)
	if err := m.applyWatchdog(ctx, s, a, sh); err != nil {
		m.fail(s, a, err)
		return
	}

	m.phase(s, PhaseDeploying)
	settings := map[string]string{"llama_cuda_url": m.Settings.LlamaCUDAURL}
	env := &plugins.Env{
		Values:   s.Values,
		Shell:    sh,
		Spec:     s.Spec,
		Status:   st,
		Lang:     m.Lang(),
		HFToken:  m.HFToken(),
		Settings: settings,
		Log:      func(f string, args ...any) { m.logf(s, f, args...) },
		Phase:    func(p string) { m.Post(func() { s.PluginPhase = p }) },
	}
	res, err := p.Deploy(ctx, env)
	if err != nil {
		m.fail(s, a, err)
		return
	}
	m.Post(func() { s.Result = res; m.saveState() })

	m.phase(s, PhaseForwarding)
	m.forwardAll(s, res.Ports, nil)
	m.phase(s, PhaseRunning)
	m.logf(s, "✓ Ready.")
	m.Post(func() { m.Notify("success", s.Title(), s.OpenAI()) })
}

func orNone(name, mem string) string {
	if name == "" {
		return "no GPU"
	}
	return name + " " + mem
}

// connect opens a shell on the runtime and puts the agent there.
func (m *Manager) connect(ctx context.Context, s *Session, conn *colab.Connection) (*colab.Shell, *agent.Status, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var sh *colab.Shell
	var err error
	// A new runtime's proxy may take a moment to answer.
	for i := 0; i < 6; i++ {
		sh, err = colab.OpenShell(cctx, conn)
		if err == nil {
			break
		}
		select {
		case <-cctx.Done():
			return nil, nil, err
		case <-time.After(5 * time.Second):
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if err := agent.Install(cctx, sh); err != nil {
		sh.Close()
		return nil, nil, err
	}
	st, err := agent.GetStatus(cctx, sh)
	if err != nil {
		sh.Close()
		return nil, nil, err
	}
	s.mu.Lock()
	if s.shell != nil {
		s.shell.Close()
	}
	s.shell = sh
	s.conn = conn
	s.mu.Unlock()
	return sh, st, nil
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

func (m *Manager) applyWatchdog(ctx context.Context, s *Session, a *Account, sh *colab.Shell) error {
	var w agent.Watchdog
	done := make(chan struct{})
	m.Post(func() {
		w = watchdogFor(&s.sessionRecord, a.Quota, time.Now())
		close(done)
	})
	<-done
	if err := agent.SetWatchdog(ctx, sh, w); err != nil {
		return err
	}
	if !s.Guards.Any() {
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
			// The rate may have changed: tell the watchdog.
			w := watchdogFor(&s.sessionRecord, q, time.Now())
			go func() {
				s.mu.Lock()
				sh := s.shell
				s.mu.Unlock()
				if sh == nil {
					return
				}
				ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
				defer cancel()
				agent.SetWatchdog(ctx, sh, w)
			}()
		}
	}
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
	id := s.RuntimeID
	s.mu.Unlock()
	go func() {
		s.closeConns()
		var err error
		if id != "" && a != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			err = a.Client.DeleteRuntime(ctx, id)
			cancel()
		}
		m.Post(func() {
			if err != nil {
				s.Phase = PhaseRunning
				s.Err = errText(err)
				m.Notify("error", s.Title(), s.Err)
				return
			}
			s.Phase = PhaseStopped
			s.StoppedAt = time.Now()
			s.Forwards = nil
			m.saveState()
			m.Notify("info", s.Title(), "")
		})
		if a != nil {
			m.refreshAccount(a)
		}
	}()
}

func (s *Session) closeConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.fwds {
		f.Close()
	}
	s.fwds = nil
	if s.mux != nil {
		s.mux.Close()
		s.mux = nil
	}
	if s.shell != nil {
		s.shell.Close()
		s.shell = nil
	}
}

// dialMux returns the session's tunnel, opening it if it isn't open.
func (s *Session) dialMux() (*tunnel.Mux, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mux != nil {
		select {
		case <-s.mux.Done():
			s.mux = nil
		default:
			return s.mux, nil
		}
	}
	if s.conn == nil {
		return nil, errors.New("not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tty, r, err := colab.Stream(ctx, s.conn, agent.MuxCommand, agent.MuxReady)
	if err != nil {
		return nil, err
	}
	s.mux = tunnel.New(tty, r)
	return s.mux, nil
}

// forwardAll forwards ports of the runtime to 127.0.0.1, falling back to
// any free port when one is taken.
func (m *Manager) forwardAll(s *Session, ports, extra []plugins.Port) {
	var states []*ForwardState
	for i, p := range append(append([]plugins.Port(nil), ports...), extra...) {
		fs := &ForwardState{Port: p, Extra: i >= len(ports)}
		local := fmt.Sprintf("127.0.0.1:%d", p.Local)
		f, err := tunnel.Listen(local, p.Remote, s.dialMux)
		if err != nil {
			m.logf(s, "Port %d is taken here; using another.", p.Local)
			f, err = tunnel.Listen("127.0.0.1:0", p.Remote, s.dialMux)
		}
		if err != nil {
			fs.Err = err.Error()
		} else {
			fs.f = f
			s.mu.Lock()
			s.fwds = append(s.fwds, f)
			s.mu.Unlock()
			m.logf(s, "Forwarding %s → runtime:%d (%s)", f.Local, p.Remote, p.Name)
		}
		states = append(states, fs)
	}
	m.Post(func() { s.Forwards = append(s.Forwards, states...) })
	// Open the tunnel now, so the first request doesn't wait for it.
	go s.dialMux()
}

// AddForward forwards another port of a running session's runtime.
func (m *Manager) AddForward(s *Session, remote, local int, name string) {
	p := plugins.Port{Name: name, Remote: remote, Local: local, Kind: plugins.PortTCP}
	if name == "" {
		p.Name = fmt.Sprintf("port %d", remote)
	}
	s.Extra = append(s.Extra, p)
	m.saveState()
	go m.forwardAll(s, nil, []plugins.Port{p})
}

// RemoveForward stops a forward the user added.
func (m *Manager) RemoveForward(s *Session, f *ForwardState) {
	if f.f != nil {
		f.f.Close()
	}
	for i, x := range s.Forwards {
		if x == f {
			s.Forwards = append(s.Forwards[:i], s.Forwards[i+1:]...)
			break
		}
	}
	for i, p := range s.Extra {
		if p == f.Port {
			s.Extra = append(s.Extra[:i], s.Extra[i+1:]...)
			break
		}
	}
	m.saveState()
}

// shellOf returns the session's shell, opening one with a fresh connection
// when it has none or its connection is about to expire.
func (m *Manager) shellOf(ctx context.Context, s *Session, a *Account) (*colab.Shell, error) {
	s.mu.Lock()
	sh, conn := s.shell, s.conn
	s.mu.Unlock()
	if sh != nil && conn != nil && time.Until(conn.ExpireTime) > 5*time.Minute {
		select {
		case <-sh.Done():
		default:
			return sh, nil
		}
	}
	rt, err := a.Client.Runtime(ctx, s.RuntimeID)
	if err != nil {
		return nil, err
	}
	if rt.Connection == nil {
		return nil, errors.New("the runtime has no connection")
	}
	if sh != nil {
		select {
		case <-sh.Done():
		default:
			// Still open: keep it, with the new token for what dials next.
			s.mu.Lock()
			s.conn = rt.Connection
			s.mu.Unlock()
			return sh, nil
		}
	}
	sh, _, err = m.connect(ctx, s, rt.Connection)
	return sh, err
}

// watch looks at a running session's runtime: is it still there, what
// does it use, did its watchdog stop it.
func (m *Manager) watch(s *Session) {
	if !s.busy.TryLock() {
		return
	}
	defer s.busy.Unlock()
	a := m.Account(s.Account)
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 90*time.Second)
	defer cancel()
	sh, err := m.shellOf(ctx, s, a)
	if errors.Is(err, colab.ErrNotFound) {
		m.gone(s)
		return
	}
	if err != nil {
		return
	}
	st, err := agent.GetStatus(ctx, sh)
	if err != nil {
		return
	}
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	res, _ := a.Client.Resources(ctx, conn)
	m.Post(func() {
		s.Status = st
		s.Err = ""
		if res != nil {
			s.Resources = res
		}
		// Reconnecting after a restart failed before its forwards: make
		// them now that the runtime answers.
		if len(s.Forwards) == 0 && s.Result != nil && len(s.Result.Ports)+len(s.Extra) > 0 {
			go m.forwardAll(s, s.Result.Ports, s.Extra)
		}
	})
}

// gone marks a session whose runtime no longer exists.
func (m *Manager) gone(s *Session) {
	s.closeConns()
	m.Post(func() {
		if s.Ended() {
			return
		}
		s.Phase = PhaseStopped
		s.StoppedAt = time.Now()
		s.StopReason = "gone"
		if st := s.Status; st != nil && st.Stopped != nil {
			s.StopReason = st.Stopped.Reason
		}
		s.Forwards = nil
		m.saveState()
		m.Notify("info", s.Title(), s.StopReason)
	})
}

// reattach finds a kept session's runtime after the app restarted, and
// forwards its ports again.
func (m *Manager) reattach(s *Session) {
	a := m.Account(s.Account)
	if a == nil || s.RuntimeID == "" {
		m.Post(func() { s.Phase = PhaseStopped; s.StopReason = "gone"; m.saveState() })
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()
	rt, err := a.Client.Runtime(ctx, s.RuntimeID)
	if errors.Is(err, colab.ErrNotFound) {
		m.gone(s)
		return
	}
	if err != nil || rt.Connection == nil {
		m.Post(func() { s.Phase = PhaseRunning; s.Err = errText(err) })
		return
	}
	_, st, err := m.connect(ctx, s, rt.Connection)
	if err != nil {
		m.Post(func() { s.Phase = PhaseRunning; s.Err = errText(err) })
		return
	}
	m.Post(func() { s.Status = st; s.Err = "" })
	var ports []plugins.Port
	if s.Result != nil {
		ports = s.Result.Ports
	}
	m.forwardAll(s, ports, s.Extra)
	m.phase(s, PhaseRunning)
	m.logf(s, "Reconnected to runtime %s.", s.RuntimeID)
}

// Logs fetches the end of the session's plugin log from the runtime.
func (m *Manager) Logs(s *Session, done func(string, error)) {
	go func() {
		a := m.Account(s.Account)
		if a == nil {
			return
		}
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		sh, err := m.shellOf(ctx, s, a)
		var out string
		if err == nil {
			out, err = agent.Logs(ctx, sh, s.Plugin, 300)
		}
		m.Post(func() { done(out, err) })
	}()
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
