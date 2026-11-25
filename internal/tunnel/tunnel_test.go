package tunnel

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"colaunch/internal/agent"
)

// pipeTransport writes to the agent's stdin.
type pipeTransport struct {
	w   io.WriteCloser
	cmd *exec.Cmd
}

func (p *pipeTransport) WriteString(s string) error { _, err := io.WriteString(p.w, s); return err }
func (p *pipeTransport) Close() error               { p.w.Close(); return p.cmd.Process.Kill() }

func python(t *testing.T) string {
	if p := os.Getenv("COLAUNCH_TEST_PYTHON"); p != "" {
		return p
	}
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("python3"); err == nil {
			return p
		}
	}
	t.Skip("no Python: set COLAUNCH_TEST_PYTHON")
	return ""
}

// startMux runs agent.py's mux as the runtime would, over pipes in place
// of a terminal.
func startMux(t *testing.T) *Mux {
	dir := t.TempDir()
	script := dir + "/agent.py"
	if err := os.WriteFile(script, agent.Script, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python(t), "-u", script, "mux")
	cmd.Env = append(os.Environ(), "COLAUNCH_HOME="+dir)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	line, err := r.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != agent.MuxReady {
		t.Fatalf("agent said %q, %v", line, err)
	}
	m := New(&pipeTransport{stdin, cmd}, r)
	t.Cleanup(func() { m.Close() })
	return m
}

func echoServer(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// Bytes sent through a forwarded port come back from the runtime's
// service unchanged, on several connections at once.
func TestForwardThroughAgent(t *testing.T) {
	m := startMux(t)
	port := echoServer(t)
	f, err := Listen("127.0.0.1:0", port, func() (*Mux, error) { return m, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", f.Local)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			want := make([]byte, 200<<10+i)
			rand.Read(want)
			go c.Write(want)
			got := make([]byte, len(want))
			c.SetReadDeadline(time.Now().Add(20 * time.Second))
			if _, err := io.ReadFull(c, got); err != nil {
				t.Errorf("conn %d: %v", i, err)
				return
			}
			if !bytes.Equal(got, want) {
				t.Errorf("conn %d: bytes changed on the way", i)
			}
		}()
	}
	wg.Wait()
}

// A port nothing listens on fails to open instead of hanging.
func TestDialClosedPort(t *testing.T) {
	m := startMux(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if _, err := m.Dial(port); err == nil {
		t.Fatal(fmt.Sprintf("dialing closed port %d worked", port))
	}
}
