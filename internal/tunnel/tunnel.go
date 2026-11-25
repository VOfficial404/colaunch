// Package tunnel carries TCP connections from 127.0.0.1 on this machine to
// ports of a runtime, over the lines of text the agent's mux reads and
// writes on a terminal of the runtime (see agent.py, class Mux).
package tunnel

import (
	"bufio"
	"bytes"
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

// ErrClosed is returned once the mux is closed.
var ErrClosed = errors.New("tunnel: closed")

// Mux carries the streams of one terminal.
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
}

// New starts a mux over a transport whose agent has said it's ready; r
// reads what the agent prints.
func New(t Transport, r *bufio.Reader) *Mux {
	m := &Mux{t: t, streams: map[string]*Stream{}, done: make(chan struct{})}
	m.lastPong.Store(time.Now().Unix())
	go m.readLoop(r)
	go m.pingLoop()
	return m
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
			s.closeRemote(err)
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
	t := time.NewTicker(20 * time.Second)
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
	case "K", "E", "D", "C":
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
			s.closeRemote(io.EOF)
		case "D":
			b, err := base64.StdEncoding.DecodeString(arg)
			if err == nil {
				m.BytesIn.Add(int64(len(b)))
				s.push(b)
			}
		case "C":
			s.closeRemote(io.EOF)
		}
	}
}

// Dial opens a connection to a port of the runtime, on its 127.0.0.1.
func (m *Mux) Dial(port int) (*Stream, error) {
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

	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	err    error
	closed bool
}

func (s *Stream) push(b []byte) {
	s.mu.Lock()
	s.buf.Write(b)
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *Stream) closeRemote(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Read reads what the runtime's side sent.
func (s *Stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.buf.Len() == 0 && s.err == nil {
		s.cond.Wait()
	}
	if s.buf.Len() > 0 {
		return s.buf.Read(p)
	}
	return 0, s.err
}

// Write sends p to the runtime's side.
func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	const chunk = 24 << 10
	n := 0
	for len(p) > 0 {
		k := min(len(p), chunk)
		if err := s.m.send("D " + s.id + " " + base64.StdEncoding.EncodeToString(p[:k])); err != nil {
			return n, err
		}
		s.m.BytesOut.Add(int64(k))
		n += k
		p = p[k:]
	}
	return n, nil
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
	if live {
		s.m.send("C " + s.id)
	}
	return nil
}

// Forward is a port of this machine forwarded to one of the runtime.
type Forward struct {
	Local  string // as 127.0.0.1:8080
	Remote int
	ln     net.Listener
	conns  atomic.Int32
}

// Conns is how many connections are open.
func (f *Forward) Conns() int { return int(f.conns.Load()) }

// Close stops listening.
func (f *Forward) Close() error { return f.ln.Close() }

// Listen forwards local, as "127.0.0.1:8080" or ":0" for any port, to port
// remote of the runtime, until the forward or the mux closes. dial returns
// the mux to use, which may change as the app reconnects.
func Listen(local string, remote int, dial func() (*Mux, error)) (*Forward, error) {
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
	f := &Forward{Local: ln.Addr().String(), Remote: remote, ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, dial)
		}
	}()
	return f, nil
}

func (f *Forward) serve(c net.Conn, dial func() (*Mux, error)) {
	defer c.Close()
	m, err := dial()
	if err != nil {
		return
	}
	s, err := m.Dial(f.Remote)
	if err != nil {
		return
	}
	defer s.Close()
	f.conns.Add(1)
	defer f.conns.Add(-1)
	done := make(chan struct{}, 2)
	go func() { io.Copy(s, c); done <- struct{}{} }()
	go func() { io.Copy(c, s); done <- struct{}{} }()
	<-done
}
