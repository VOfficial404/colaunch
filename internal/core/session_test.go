package core

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/tunnel"
)

type nopTransport struct{ r *io.PipeWriter }

func (nopTransport) WriteString(string) error                         { return nil }
func (nopTransport) WriteStringContext(context.Context, string) error { return nil }
func (t nopTransport) Close() error                                   { return t.r.Close() }

// fakeStream replaces the mux's terminal with one that answers after
// release is closed, counting the dials.
func fakeStream(t *testing.T, release chan struct{}) *atomic.Int32 {
	var dials atomic.Int32
	old := streamMux
	streamMux = func(ctx context.Context, conn *colab.Connection) (tunnel.Transport, *bufio.Reader, error) {
		dials.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		r, w := io.Pipe()
		return nopTransport{w}, bufio.NewReader(r), nil
	}
	t.Cleanup(func() { streamMux = old })
	return &dials
}

// Opening the tunnel doesn't hold the session's lock, and connections
// coming at once share one dial.
func TestDialMuxOutsideLock(t *testing.T) {
	release := make(chan struct{})
	dials := fakeStream(t, release)
	s := &Session{}
	s.conn = &colab.Connection{ExpireTime: time.Now().Add(time.Hour)}
	var wg sync.WaitGroup
	muxes := make([]*tunnel.Mux, 3)
	for i := range muxes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := s.dialMux(t.Context(), 0)
			if err != nil {
				t.Error(err)
			}
			muxes[i] = m
		}()
	}
	for dials.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	locked := make(chan struct{})
	go func() { s.mu.Lock(); s.mu.Unlock(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("the lock is held while dialing")
	}
	close(release)
	wg.Wait()
	if dials.Load() != 1 || muxes[0] == nil || muxes[0] != muxes[1] || muxes[1] != muxes[2] {
		t.Errorf("%d dials for 3 connections", dials.Load())
	}
	s.closeConns()
}

// Closing the session while the tunnel dials stops the dial, and drops
// what it opens.
func TestCloseConnsStopsDial(t *testing.T) {
	release := make(chan struct{})
	dials := fakeStream(t, release)
	s := &Session{}
	s.conn = &colab.Connection{}
	errc := make(chan error, 1)
	go func() { _, err := s.dialMux(t.Context(), 0); errc <- err }()
	for dials.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	closed := make(chan struct{})
	go func() { s.closeConns(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("closeConns waited for the dial")
	}
	select {
	case err := <-errc:
		if err == nil {
			t.Error("the dial worked across closeConns")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the dial went on")
	}
	if s.mux != nil {
		t.Error("the session kept a tunnel")
	}
}

// Removing a forward forgets it, so that closing the session doesn't
// close it again and it isn't kept.
func TestRemoveForward(t *testing.T) {
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	s := &Session{Phase: PhaseRunning}
	f, err := tunnel.Listen("127.0.0.1:0", 80, tunnel.ForwardDialFunc(func(ctx context.Context, port int) (io.ReadWriteCloser, error) {
		return nil, errors.New("unused")
	}), tunnel.ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.fwds = []*tunnel.Forward{f}
	fs := &ForwardState{f: f, Extra: true}
	s.Forwards = []*ForwardState{fs}
	m.RemoveForward(s, fs)
	if len(s.fwds) != 0 || len(s.Forwards) != 0 {
		t.Errorf("%d forwards kept, %d shown", len(s.fwds), len(s.Forwards))
	}
}

// The balance coming in again tells the watchdog only when its limits
// moved by a minute or more.
func TestCheckBalanceSkipsSameWatchdog(t *testing.T) {
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	a := &Account{accountRecord: accountRecord{Email: "a@example.com"}, Quota: &colab.Quota{Balance: 95, RateHourly: 4}}
	s := &Session{Phase: PhaseRunning, sessionRecord: sessionRecord{Account: a.Email, StartBalance: 100, Guards: Guards{SpendCap: 10}}}
	m.Accounts, m.Sessions = []*Account{a}, []*Session{s}
	w := watchdogFor(&s.sessionRecord, a.Quota, time.Now().Add(-30*time.Second))
	s.sentWD = &w
	m.checkBalance(a)
	if s.sentWD != &w {
		t.Error("told the watchdog a budget 30 s later")
	}
	a.Quota = &colab.Quota{Balance: 95, RateHourly: 8}
	m.checkBalance(a)
	if s.sentWD == &w {
		t.Error("didn't tell the watchdog of a new rate")
	}
	if !sameWatchdog(&agent.Watchdog{BudgetAt: 1000}, agent.Watchdog{BudgetAt: 1059}) ||
		sameWatchdog(&agent.Watchdog{BudgetAt: 1000}, agent.Watchdog{BudgetAt: 1060}) ||
		sameWatchdog(&agent.Watchdog{BudgetAt: 1000, IdleMinutes: 5}, agent.Watchdog{BudgetAt: 1000}) ||
		sameWatchdog(&agent.Watchdog{BudgetAt: 30}, agent.Watchdog{}) {
		t.Error("sameWatchdog")
	}
}

// Balances are polled only while a session is live, in the background
// only those of its account; runtimes only while running.
func TestLoopDue(t *testing.T) {
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	a1, a2 := acct("a1"), acct("a2")
	m.Accounts = []*Account{a1, a2}
	m.Sessions = []*Session{{Phase: PhaseStopped, sessionRecord: sessionRecord{Account: "a1"}}}
	if accounts, sessions := m.due(false, true, true); len(accounts)+len(sessions) != 0 {
		t.Errorf("no live session: %d accounts, %d runtimes", len(accounts), len(sessions))
	}
	running := &Session{Phase: PhaseRunning, sessionRecord: sessionRecord{Account: "a2"}}
	m.Sessions = append(m.Sessions, running)
	if accounts, sessions := m.due(false, true, true); len(accounts) != 2 || len(sessions) != 1 || sessions[0].a != a2 {
		t.Errorf("foreground: %d accounts, %v", len(accounts), sessions)
	}
	if accounts, _ := m.due(true, true, true); len(accounts) != 1 || accounts[0] != a2 {
		t.Errorf("background: %v", accounts)
	}
	if _, sessions := m.due(false, false, true); len(sessions) != 0 {
		t.Error("watched runtimes on a balance tick")
	}
}

// The loop slows down in the background and looks at once when it comes
// back.
func TestSetBackground(t *testing.T) {
	oldTick, oldBg := loopTick, backgroundTick
	loopTick, backgroundTick = time.Hour, time.Hour
	t.Cleanup(func() { loopTick, backgroundTick = oldTick, oldBg })
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	var peeks atomic.Int32
	m.Peek = func(fn func()) { peeks.Add(1); fn() }
	go m.loop()
	m.SetBackground(true)
	time.Sleep(50 * time.Millisecond)
	if peeks.Load() != 0 {
		t.Error("going to the background looked at the runtimes")
	}
	m.SetBackground(false)
	deadline := time.Now().Add(2 * time.Second)
	for peeks.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if peeks.Load() != 1 {
		t.Errorf("%d looks on coming back", peeks.Load())
	}
}

// Log lines that come together are shown with one Post.
func TestLogBatches(t *testing.T) {
	m := New(t.TempDir(), mapSecrets{})
	defer m.Close()
	var mu sync.Mutex
	posts := 0
	m.Post = func(fn func()) { mu.Lock(); posts++; fn(); mu.Unlock() }
	s := &Session{}
	for i := range 50 {
		m.logf(s, "line %d", i)
	}
	time.Sleep(3 * logBatch)
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || len(s.Log) != 50 || s.Log[49].Text != "line 49" {
		t.Errorf("%d posts for %d lines", posts, len(s.Log))
	}
}

func acct(email string) *Account {
	a := &Account{}
	a.Email = email
	return a
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
