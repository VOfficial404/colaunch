// Package tunnel forwards local TCP connections to ports on a runtime.
// Mux implements the legacy terminal protocol used by agent.py.
package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Transport is what the mux speaks over: a terminal on the runtime.
type Transport interface {
	WriteString(s string) error
	Close() error
}

var (
	// ErrClosed is returned once the mux is closed.
	ErrClosed = errors.New("tunnel: closed")
	// ErrIdle is why a mux closed that carried nothing for its idle time.
	ErrIdle = errors.New("tunnel: idle")
	// ErrStalled is why a stream closed whose reader stopped reading.
	ErrStalled = errors.New("tunnel: the connection stopped reading")
)

// Limits of a stream, variables for the tests.
var (
	// MaxBuffer is how much of what the runtime sent a stream holds for
	// its reader. Past it, the mux stops reading the terminal, all its
	// streams waiting, until the reader takes some; the agent has no way
	// to pause one stream alone.
	MaxBuffer = 4 << 20
	// StallTimeout is how long the mux waits for such a reader before
	// closing its stream, so that one connection doesn't stop the others
	// for long.
	StallTimeout = 30 * time.Second
	// HalfCloseTimeout is how long a connection that one side ended stays
	// open with nothing coming from the other.
	HalfCloseTimeout = 30 * time.Second
)

// Mux carries the streams of one terminal.
//
// The protocol is agent.py's: app → agent O, D, C, P; agent → app K, E, D,
// C, P. W id ends one direction of a stream, the sender's: the agent may
// send it when its socket reads EOF, and gets it when the app's side does
// (an agent that doesn't know W ignores it, its socket staying open).
type Mux struct {
	t       Transport
	wmu     sync.Mutex
	mu      sync.Mutex
	streams map[string]*Stream
	next    atomic.Uint64
	done    chan struct{}
	err     error
	once    sync.Once

	// BytesIn and BytesOut count what went through.
	BytesIn, BytesOut atomic.Int64
	lastPong          atomic.Int64
	// lastActive is when, in Unix nanoseconds, a stream last opened,
	// closed or carried data; idle is how long to stay open without.
	lastActive atomic.Int64
	idle       atomic.Int64
	// tick is how often pingLoop runs, for the tests.
	tick time.Duration
}

// New starts a mux over a transport whose agent has said it's ready; r
// reads what the agent prints.
func New(t Transport, r *bufio.Reader) *Mux {
	return newMux(t, r, 20*time.Second)
}

func newMux(t Transport, r *bufio.Reader, tick time.Duration) *Mux {
	m := &Mux{t: t, streams: map[string]*Stream{}, done: make(chan struct{}), tick: tick}
	m.lastPong.Store(time.Now().Unix())
	m.active()
	go m.readLoop(r)
	go m.pingLoop()
	return m
}

// SetIdleTimeout has the mux close itself, with ErrIdle, once it has had
// no stream for d; 0 keeps it open.
func (m *Mux) SetIdleTimeout(d time.Duration) { m.idle.Store(int64(d)) }

func (m *Mux) active() { m.lastActive.Store(time.Now().UnixNano()) }

// Streams is how many streams are open.
func (m *Mux) Streams() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.streams)
}

// Done is closed once the mux is.
func (m *Mux) Done() <-chan struct{} { return m.done }

// Err is why the mux closed.
func (m *Mux) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Close closes the mux and its streams.
func (m *Mux) Close() error {
	m.close(ErrClosed)
	return nil
}

func (m *Mux) close(err error) {
	m.once.Do(func() {
		m.mu.Lock()
		m.err = err
		streams := m.streams
		m.streams = map[string]*Stream{}
		m.mu.Unlock()
		close(m.done)
		for _, s := range streams {
			s.closeRemote(err, true)
		}
		m.t.Close()
	})
}

func (m *Mux) send(line string) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	select {
	case <-m.done:
		return ErrClosed
	default:
	}
	if err := m.t.WriteString(line + "\n"); err != nil {
		m.close(err)
		return err
	}
	return nil
}

func (m *Mux) pingLoop() {
	t := time.NewTicker(m.tick)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-t.C:
			if time.Now().Unix()-m.lastPong.Load() > 90 {
				m.close(errors.New("tunnel: the runtime stopped answering"))
				return
			}
			if idle := time.Duration(m.idle.Load()); idle > 0 && m.Streams() == 0 &&
				time.Since(time.Unix(0, m.lastActive.Load())) > idle {
				m.close(ErrIdle)
				return
			}
			m.send("P")
		}
	}
}

func (m *Mux) readLoop(r *bufio.Reader) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			m.close(fmt.Errorf("tunnel: %w", err))
			return
		}
		m.handle(strings.TrimRight(line, "\r\n"))
	}
}

func (m *Mux) handle(line string) {
	op, rest, _ := strings.Cut(line, " ")
	id, arg, _ := strings.Cut(rest, " ")
	switch op {
	case "P":
		m.lastPong.Store(time.Now().Unix())
	case "K", "E", "D", "C", "W":
		m.mu.Lock()
		s := m.streams[id]
		if op == "C" || op == "E" {
			delete(m.streams, id)
		}
		m.mu.Unlock()
		if s == nil {
			return
		}
		switch op {
		case "K":
			s.opened <- nil
		case "E":
			s.opened <- errors.New("tunnel: " + arg)
			s.closeRemote(io.EOF, true)
		case "D":
			b, err := base64.StdEncoding.DecodeString(arg)
			if err == nil {
				m.BytesIn.Add(int64(len(b)))
				m.active()
				if !s.push(b) {
					s.abort(ErrStalled)
				}
			}
		case "W":
			s.closeRemote(io.EOF, false)
		case "C":
			m.active()
			s.closeRemote(io.EOF, true)
		}
	}
}

// Dial opens a connection to a port of the runtime, on its 127.0.0.1.
func (m *Mux) Dial(port int) (*Stream, error) {
	return m.dial(context.Background(), port)
}

// DialPort implements ForwardDialer for the legacy terminal mux.
func (m *Mux) DialPort(ctx context.Context, port int) (io.ReadWriteCloser, error) {
	s, err := m.dial(ctx, port)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (m *Mux) dial(ctx context.Context, port int) (*Stream, error) {
	id := strconv.FormatUint(m.next.Add(1), 36)
	s := &Stream{m: m, id: id, opened: make(chan error, 1)}
	s.cond = sync.NewCond(&s.mu)
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return nil, m.err
	}
	m.streams[id] = s
	m.mu.Unlock()
	m.active()
	if err := m.send(fmt.Sprintf("O %s %d", id, port)); err != nil {
		return nil, err
	}
	select {
	case err := <-s.opened:
		if err != nil {
			return nil, err
		}
		return s, nil
	case <-m.done:
		return nil, m.Err()
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		s.Close()
		return nil, errors.New("tunnel: the runtime didn't answer")
	}
}

// Stream is a connection through the mux.
type Stream struct {
	m      *Mux
	id     string
	opened chan error

	mu   sync.Mutex
	cond *sync.Cond
	buf  bytes.Buffer
	// err ends reading, once buf is read; gone is true once the runtime's
	// side closed both ways, or the mux did, so writing fails too.
	err    error
	gone   bool
	shut   bool // this side's writing ended
	closed bool
}

// push adds what the runtime sent, waiting while the stream holds
// MaxBuffer already, StallTimeout at the most: it returns false when the
// reader didn't make room in time.
func (s *Stream) push(b []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	full := func() bool {
		return s.buf.Len() > 0 && s.buf.Len()+len(b) > MaxBuffer && !s.closed && s.err == nil
	}
	if full() {
		timedOut := false
		t := time.AfterFunc(StallTimeout, func() {
			s.mu.Lock()
			timedOut = true
			s.cond.Broadcast()
			s.mu.Unlock()
		})
		for full() && !timedOut {
			s.cond.Wait()
		}
		t.Stop()
		if full() {
			return false
		}
	}
	if s.closed || s.err != nil {
		return true
	}
	s.buf.Write(b)
	s.cond.Broadcast()
	return true
}

// closeRemote ends reading, and with gone writing too.
func (s *Stream) closeRemote(err error, gone bool) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.gone = s.gone || gone
	s.cond.Broadcast()
	s.mu.Unlock()
}

// abort closes the stream at both ends, err telling its reader why.
func (s *Stream) abort(err error) {
	s.mu.Lock()
	s.buf.Reset()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.Close()
}

// Read reads what the runtime's side sent.
func (s *Stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.buf.Len() == 0 && s.err == nil && !s.closed {
		s.cond.Wait()
	}
	if s.buf.Len() > 0 {
		n, err := s.buf.Read(p)
		// Room for push, which may wait for it.
		s.cond.Broadcast()
		return n, err
	}
	if s.err != nil {
		return 0, s.err
	}
	return 0, io.EOF
}

// Write sends p to the runtime's side.
func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	gone, shut, closed, err := s.gone, s.shut, s.closed, s.err
	s.mu.Unlock()
	switch {
	case gone:
		return 0, err
	case shut || closed:
		return 0, ErrClosed
	}
	const chunk = 24 << 10
	n := 0
	for len(p) > 0 {
		k := min(len(p), chunk)
		if err := s.m.send("D " + s.id + " " + base64.StdEncoding.EncodeToString(p[:k])); err != nil {
			return n, err
		}
		s.m.BytesOut.Add(int64(k))
		s.m.active()
		n += k
		p = p[k:]
	}
	return n, nil
}

// CloseWrite tells the runtime's side that nothing more comes, and goes
// on reading what it sends.
func (s *Stream) CloseWrite() error {
	s.mu.Lock()
	skip := s.shut || s.closed || s.gone
	s.shut = true
	s.mu.Unlock()
	if skip {
		return nil
	}
	return s.m.send("W " + s.id)
}

// Close closes the connection at both ends.
func (s *Stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.err == nil {
		s.err = io.EOF
	}
	s.cond.Broadcast()
	s.mu.Unlock()
	s.m.mu.Lock()
	_, live := s.m.streams[s.id]
	delete(s.m.streams, s.id)
	s.m.mu.Unlock()
	s.m.active()
	if live {
		s.m.send("C " + s.id)
	}
	return nil
}

// remoteGone reports whether the runtime's side closed both ways.
func (s *Stream) remoteGone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gone
}

// ForwardDialer opens a new stream for each accepted local connection.
type ForwardDialer interface {
	DialPort(context.Context, int) (io.ReadWriteCloser, error)
}

// ForwardDialFunc adapts a function to ForwardDialer.
type ForwardDialFunc func(context.Context, int) (io.ReadWriteCloser, error)

func (f ForwardDialFunc) DialPort(ctx context.Context, port int) (io.ReadWriteCloser, error) {
	return f(ctx, port)
}

// ForwardOptions reports dial failures and traffic to the owner.
type ForwardOptions struct {
	OnDialError func(error)
	OnTraffic   func()
	DialTimeout time.Duration // zero uses one minute
}

// Forward is a port of this machine forwarded to one of the runtime.
type Forward struct {
	Local  string // as 127.0.0.1:8080
	Remote int
	ln     net.Listener
	conns  atomic.Int32

	mu            sync.Mutex
	open          map[net.Conn]io.ReadWriteCloser
	closed        bool
	lastDialError error
	cancel        context.CancelFunc
}

// Conns is how many connections are open.
func (f *Forward) Conns() int { return int(f.conns.Load()) }

// LastDialError is the most recent dial failure, cleared on a successful dial.
func (f *Forward) LastDialError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastDialError
}

// Close stops listening, and closes the connections open.
func (f *Forward) Close() error {
	f.cancel()
	err := f.ln.Close()
	f.mu.Lock()
	f.closed = true
	open := f.open
	f.open = nil
	f.mu.Unlock()
	for c, s := range open {
		c.Close()
		if s != nil {
			s.Close()
		}
	}
	return err
}

// track keeps a connection, with its stream once dialed, for Close; false
// once the forward is closed.
func (f *Forward) track(c net.Conn, s io.ReadWriteCloser) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.open[c] = s
	return true
}

func (f *Forward) untrack(c net.Conn) {
	f.mu.Lock()
	delete(f.open, c)
	f.mu.Unlock()
}

// Listen forwards local to the runtime's port until the listener closes.
// Each incoming connection dials through the current remote transport.
func Listen(local string, remote int, dial ForwardDialer, opts ForwardOptions) (*Forward, error) {
	if !strings.Contains(local, ":") {
		local = "127.0.0.1:" + local
	}
	if strings.HasPrefix(local, ":") {
		local = "127.0.0.1" + local
	}
	ln, err := net.Listen("tcp", local)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &Forward{Local: ln.Addr().String(), Remote: remote, ln: ln, open: map[net.Conn]io.ReadWriteCloser{}, cancel: cancel}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(ctx, c, dial, opts)
		}
	}()
	return f, nil
}

func (f *Forward) serve(ctx context.Context, c net.Conn, dial ForwardDialer, opts ForwardOptions) {
	defer c.Close()
	if !f.track(c, nil) {
		return
	}
	defer f.untrack(c)
	timeout := opts.DialTimeout
	if timeout == 0 {
		timeout = time.Minute
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	s, err := dial.DialPort(dialCtx, f.Remote)
	cancel()
	f.mu.Lock()
	var notify bool
	if ctx.Err() == nil {
		notify = err != nil && (f.lastDialError == nil || f.lastDialError.Error() != err.Error())
		f.lastDialError = err
	}
	f.mu.Unlock()
	if err != nil {
		if notify && opts.OnDialError != nil {
			opts.OnDialError(err)
		}
		return
	}
	defer s.Close()
	if !f.track(c, s) {
		return
	}
	f.conns.Add(1)
	defer f.conns.Add(-1)

	// Each direction ends by itself: a side that has said all it has
	// still hears the other's answer, for HalfCloseTimeout of silence.
	var once sync.Once
	var timer *time.Timer
	halfDone := func() {
		once.Do(func() {
			timer = time.AfterFunc(HalfCloseTimeout, func() { c.Close(); s.Close() })
		})
	}
	extend := func() {
		if timer != nil {
			timer.Reset(HalfCloseTimeout)
		}
	}
	var tmu sync.Mutex // timer, between the two directions
	defer func() {
		tmu.Lock()
		if timer != nil {
			timer.Stop()
		}
		tmu.Unlock()
	}()
	done := make(chan bool, 2)
	go func() {
		// Here to the runtime.
		b := make([]byte, 32<<10)
		var err error
		for {
			var n int
			n, err = c.Read(b)
			if n > 0 {
				tmu.Lock()
				extend()
				tmu.Unlock()
				if _, werr := s.Write(b[:n]); werr != nil {
					done <- false
					return
				}
				if opts.OnTraffic != nil {
					opts.OnTraffic()
				}
			}
			if err != nil {
				break
			}
		}
		if !errors.Is(err, io.EOF) {
			done <- false
			return
		}
		cw, ok := s.(interface{ CloseWrite() error })
		if !ok {
			done <- false
			return
		}
		cw.CloseWrite()
		done <- true
	}()
	go func() {
		// The runtime to here.
		b := make([]byte, 32<<10)
		for {
			n, err := s.Read(b)
			if n > 0 {
				tmu.Lock()
				extend()
				tmu.Unlock()
				if _, werr := c.Write(b[:n]); werr != nil {
					done <- false
					return
				}
				if opts.OnTraffic != nil {
					opts.OnTraffic()
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					done <- false
					return
				}
				break
			}
		}
		cw, ok := c.(interface{ CloseWrite() error })
		legacy, isLegacy := s.(*Stream)
		if !ok || (isLegacy && legacy.remoteGone()) {
			done <- false
			return
		}
		cw.CloseWrite()
		done <- true
	}()
	for range 2 {
		if !<-done {
			return
		}
		tmu.Lock()
		halfDone()
		tmu.Unlock()
	}
}
