package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/tunnel"
)

func fakeColab(t *testing.T) *colab.Connection { return Colab(t, nil) }

// Commands run on the terminal give back what they printed and their
// status, apart from the terminal's echo and prompts.
func TestShellRun(t *testing.T) {
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	out, code, err := sh.Run(ctx, "echo hello; echo 'it''s' \"$((6*7))\" >&2; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 || strings.TrimSpace(out) != "hello\nits 42" {
		t.Errorf("got %q, status %d", out, code)
	}
	// A command longer than a terminal's line goes up as a file.
	long := "x=" + strings.Repeat("a", 5000) + "; echo ${#x}"
	out, code, err = sh.Run(ctx, long)
	if err != nil || code != 0 || strings.TrimSpace(out) != "5000" {
		t.Errorf("long command: %q, %d, %v", out, code, err)
	}
	// Commands keep their order and don't read each other's output.
	for i := range 5 {
		out, _, err := sh.Run(ctx, fmt.Sprintf("echo %d", i))
		if err != nil || strings.TrimSpace(out) != fmt.Sprint(i) {
			t.Errorf("command %d: %q %v", i, out, err)
		}
	}
}

// A new terminal that first prints an earlier one's output, as an END
// with no START, still opens, and its commands read their own output: the
// shell is ready on a marker of a nonce of its own.
func TestOpenShellAfterStaleOutput(t *testing.T) {
	conn := Colab(t, func(int) Faults {
		return Faults{Stale: "@@START-1@\r\n@@END-1:0@@\r\n@@END-2:0@@\r\n@@HELLO-0123456789abcdef@@\r\n"}
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	for i := range 2 {
		out, code, err := sh.Run(ctx, fmt.Sprintf("echo %d; exit 4", i))
		if err != nil || code != 4 || strings.TrimSpace(out) != fmt.Sprint(i) {
			t.Errorf("command %d: %q, %d, %v", i, out, code, err)
		}
	}
}

// Replacing the control terminal must leave a running installer in place.
func TestDetachedPluginSurvivesTerminalReconnect(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the runtime agent checks ownership using Linux /proc")
	}
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	first, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "agent.py")
	if err := first.Upload(ctx, agentPath, agent.Script); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "install.sh")
	if err := first.Upload(ctx, scriptPath, []byte(`echo started
for ((i=0; i<150; i++)); do
  if [ -f "$COLAUNCH_HOME/finish-install" ]; then
    echo finished
    exit 0
  fi
  sleep .1
done
exit 1
`)); err != nil {
		t.Fatal(err)
	}
	runAgent := fmt.Sprintf("COLAUNCH_HOME=%q python3 %q", dir, agentPath)
	if out, code, err := first.Run(ctx, fmt.Sprintf("%s run install %q", runAgent, scriptPath)); err != nil || code != 0 {
		t.Fatalf("start installer: status %d, %q, %v", code, out, err)
	}
	finishPath := filepath.Join(dir, "finish-install")
	t.Cleanup(func() { _ = os.WriteFile(finishPath, nil, 0o600) })
	first.Close()

	second, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if out, code, err := second.Run(ctx, runAgent+" running install"); err != nil || code != 0 || strings.TrimSpace(out) != "yes" {
		t.Fatalf("after reconnect: status %d, %q, %v", code, out, err)
	}
	if err := os.WriteFile(finishPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		out, code, err := second.Run(ctx, runAgent+" logs install")
		if err != nil || code != 0 {
			t.Fatalf("read installer log: status %d, %q, %v", code, out, err)
		}
		if strings.TrimSpace(out) == "started\nfinished" {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("installer did not finish after replacing the control terminal")
}

// Files go up byte for byte, binary ones too.
func TestShellUpload(t *testing.T) {
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	data := make([]byte, 150<<10)
	rand.Read(data)
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "sub dir", "blob.bin"))
	if err := sh.Upload(ctx, path, data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	out, _, err := sh.Run(ctx, "sha256sum '"+path+"'")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), hex.EncodeToString(sum[:])) {
		t.Errorf("uploaded file's sum %q, want %x", out, sum)
	}
	// The terminal reads commands as before after a file.
	if out, _, _ := sh.Run(ctx, "echo still here"); strings.TrimSpace(out) != "still here" {
		t.Errorf("after the upload: %q", out)
	}
}

// An upload whose folder can't be made, or whose file can't be moved in
// place, fails, leaves nothing behind, and the terminal reads commands on.
func TestShellUploadFails(t *testing.T) {
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("x"), 0o644)
	if err := sh.Upload(ctx, filepath.ToSlash(file)+"/sub/blob", []byte("data")); err == nil {
		t.Error("uploading under a file worked")
	}
	// mv can't put a file in place of a folder.
	os.MkdirAll(filepath.Join(dir, "folder", "inside"), 0o755)
	if err := sh.Upload(ctx, filepath.ToSlash(filepath.Join(dir, "folder")), []byte("data")); err == nil {
		t.Error("a failed mv went unnoticed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("left behind: %v", entries)
	}
	if out, _, _ := sh.Run(ctx, "echo still here"); strings.TrimSpace(out) != "still here" {
		t.Errorf("after the failed uploads: %q", out)
	}
}

// A port of the runtime reaches 127.0.0.1 here through the agent's mux on
// a terminal of its own, the way the app forwards llama-server's.
func TestTunnelOverTerminal(t *testing.T) {
	conn := fakeColab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sh, err := colab.OpenShell(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "agent.py"))
	if err := sh.Upload(ctx, path, agent.Script); err != nil {
		t.Fatal(err)
	}
	// The runtime's service: an echo server.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	tty, r, err := colab.Stream(ctx, conn, "python3 -u '"+path+"' mux", agent.MuxReady)
	if err != nil {
		t.Fatal(err)
	}
	m := tunnel.New(tty, r)
	defer m.Close()
	f, err := tunnel.Listen("127.0.0.1:0", ln.Addr().(*net.TCPAddr).Port, m, tunnel.ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := net.Dial("tcp", f.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := make([]byte, 300<<10)
	rand.Read(want)
	go c.Write(want)
	got := make([]byte, len(want))
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("bytes changed through the terminal")
	}
}
