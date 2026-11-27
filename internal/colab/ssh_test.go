package colab_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/e2e"
	"colaunch/internal/tunnel"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

const testProxyToken = "test token+with/% reserved?&=characters"

func sshKey(t *testing.T) ssh.Signer {
	t.Helper()
	key, err := colab.NewSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := colab.ParseSSHKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// The first packet from the server is its plaintext SSH identification.
// Send it in a text message, then use binary for all subsequent messages.
type textBanner struct {
	net.Conn
	ws   *websocket.Conn
	sent bool
}

func (c *textBanner) Write(p []byte) (int, error) {
	if c.sent {
		return c.Conn.Write(p)
	}
	c.sent = true
	if err := c.ws.Write(context.Background(), websocket.MessageText, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func TestSSHWireContract(t *testing.T) {
	bash := e2e.Bash(t)
	user, host := sshKey(t), sshKey(t)
	wrongHost := sshKey(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "through SSH: "+r.URL.Path)
	}))
	t.Cleanup(remote.Close)
	remoteAddr := remote.Listener.Addr().String()
	port := remote.Listener.Addr().(*net.TCPAddr).Port
	var opened atomic.Int32
	connections := make(chan net.Conn, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/colab/ssh" || r.URL.Query().Get("colab-runtime-proxy-token") != testProxyToken ||
			!reflect.DeepEqual(r.URL.Query()["colab-client-agent"], []string{"colab-cli", "ssh-mode-proxy"}) ||
			r.Header.Get("X-Colab-Client-Agent") != "colab-cli" ||
			r.Header.Get("X-Colab-Runtime-Proxy-Token") != testProxyToken {
			t.Error("incorrect SSH WebSocket request")
			http.Error(w, "incorrect request", http.StatusBadRequest)
			return
		}
		pub, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(r.Header.Get("X-Colab-Ssh-Pubkey")))
		if err != nil || len(options) != 0 || len(rest) != 0 || pub.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(pub.Marshal(), user.PublicKey().Marshal()) {
			t.Error("incorrect OpenSSH public key line")
			http.Error(w, "incorrect pubkey", http.StatusBadRequest)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		n := opened.Add(1)
		defer ws.CloseNow()
		// NetConn also verifies that all frames from the client are binary.
		nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		connections <- nc
		cfg := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "root" || !bytes.Equal(pub.Marshal(), key.Marshal()) {
				return nil, errors.New("wrong user or key")
			}
			return nil, nil
		}}
		if n == 3 {
			cfg.AddHostKey(wrongHost)
		} else {
			cfg.AddHostKey(host)
		}
		conn, channels, requests, err := ssh.NewServerConn(&textBanner{Conn: nc, ws: ws}, cfg)
		if err != nil {
			if n != 3 {
				t.Error(err)
			}
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		for ch := range channels {
			switch ch.ChannelType() {
			case "session":
				go serveExec(t, ch, bash)
			case "direct-tcpip":
				go serveDirectTCP(t, ch, remoteAddr)
			default:
				ch.Reject(ssh.UnknownChannelType, "unsupported")
			}
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	proxy := &colab.Connection{URL: server.URL + "/ignored?old=1", Token: testProxyToken}
	opt := colab.SSHOptions{HTTPClient: server.Client()}
	client, err := colab.DialSSH(ctx, proxy, user, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	out, code, err := client.Run(ctx, `printf 'first\n'; printf 'second\n' >&2; exit 23`)
	if err != nil || code != 23 || out != "first\nsecond\n" {
		t.Fatalf("exec = %q, %d, %v", out, code, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "new folder", "quote'file")
	data := bytes.Repeat([]byte{0, '\n', 0xff, 'x'}, 1<<16)
	if err := client.Upload(ctx, filepath.ToSlash(path), data); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("upload = %d bytes, %v", len(got), err)
	}
	if runtime.GOOS != "windows" {
		for file, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			fi, err := os.Stat(file)
			if err != nil || fi.Mode().Perm() != want {
				t.Fatalf("wrong permissions for %s: %v, %v", file, fi, err)
			}
		}
	}
	if err := client.Upload(ctx, filepath.ToSlash(path), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || len(got) != 0 {
		t.Fatalf("replacement upload = %d bytes, %v", len(got), err)
	}
	destinationDir := filepath.Dir(path)
	if err := client.Upload(ctx, filepath.ToSlash(destinationDir), data); err == nil {
		t.Fatal("upload accepted a directory as the destination")
	}
	if files, err := os.ReadDir(destinationDir); err != nil || len(files) != 1 || files[0].Name() != filepath.Base(path) {
		t.Fatalf("unexpected files after upload: %v, %v", files, err)
	}
	if files, err := filepath.Glob(filepath.Join(dir, "*.colaunch-*")); err != nil || len(files) != 0 {
		t.Fatalf("temporary files left behind: %v, %v", files, err)
	}

	stream, reader, err := client.Stream(ctx, `bash -c 'echo @@READY@@; while IFS= read -r line; do printf "echo %s\n" "$line"; done'`, "@@READY@@")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := stream.WriteStringContext(ctx, "hello\n"); err != nil {
		t.Fatal(err)
	}
	if reply, err := reader.ReadString('\n'); err != nil || reply != "echo hello\n" {
		t.Fatalf("stream = %q, %v", reply, err)
	}

	var current atomic.Pointer[colab.SSH]
	current.Store(client)
	forward, err := tunnel.Listen(":0", port, tunnel.ForwardDialFunc(func(ctx context.Context, p int) (io.ReadWriteCloser, error) {
		return current.Load().DialPort(ctx, p)
	}), tunnel.ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()
	httpClient := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	checkForward := func() {
		t.Helper()
		resp, err := httpClient.Get("http://" + forward.Local + "/object_info")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "through SSH: /object_info" {
			t.Fatalf("HTTP through SSH = %q, %v", body, err)
		}
	}
	checkForward()
	if opened.Load() != 1 {
		t.Fatal("commands, monitor and forward did not reuse the same connection")
	}
	opt.HostKey = client.HostKey()
	(<-connections).Close()
	select {
	case <-client.Done():
	case <-ctx.Done():
		t.Fatal("Done never closed after the proxy dropped")
	}
	reconnected, err := colab.DialSSH(ctx, proxy, user, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	current.Store(reconnected)
	checkForward()
	if _, err := colab.DialSSH(ctx, proxy, user, opt); !errors.Is(err, colab.ErrSSHHostKey) {
		t.Fatalf("changed host key: %v", err)
	}
}

func serveExec(t *testing.T, pending ssh.NewChannel, bash string) {
	ch, requests, err := pending.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	for req := range requests {
		if req.Type != "exec" {
			t.Errorf("unexpected session request %q", req.Type)
			req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			req.Reply(false, nil)
			return
		}
		req.Reply(true, nil)
		// Exercise the BSD mv option set even when the fixture runs with GNU mv.
		const bsdMV = `mv() {
  case "$1" in -*T*) return 64 ;; esac
  command mv "$@"
}
export -f mv
`
		cmd := exec.Command(bash, "-c", bsdMV+payload.Command)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
		var code uint32
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Error(err)
				return
			}
			code = uint32(exit.ExitCode())
		}
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
		return
	}
}

func serveDirectTCP(t *testing.T, pending ssh.NewChannel, addr string) {
	var req struct {
		Host       string
		Port       uint32
		Origin     string
		OriginPort uint32
	}
	err := ssh.Unmarshal(pending.ExtraData(), &req)
	if err != nil || net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port))) != addr {
		t.Error("unexpected direct-tcpip target")
		pending.Reject(ssh.Prohibited, "unexpected target")
		return
	}
	remote, err := net.Dial("tcp", addr)
	if err != nil {
		pending.Reject(ssh.ConnectionFailed, "could not dial target")
		return
	}
	defer remote.Close()
	ch, requests, err := pending.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(requests)
	go func() {
		io.Copy(remote, ch)
		remote.(*net.TCPConn).CloseWrite()
	}()
	io.Copy(ch, remote)
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSSHUpgradeErrors(t *testing.T) {
	key := sshKey(t)
	for _, status := range []int{400, 401, 403, 404, 405, 429, 501, 502} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"error":"already-active-session","limit":5,"request":%q}`, r.URL.String())
			}))
			defer srv.Close()
			_, err := colab.DialSSH(t.Context(), &colab.Connection{URL: srv.URL, Token: testProxyToken}, key, colab.SSHOptions{HTTPClient: srv.Client()})
			var upgrade *colab.SSHError
			if !errors.As(err, &upgrade) || upgrade.Status != status || upgrade.Fallback() != (status == 404 || status == 405 || status == 501) {
				t.Fatalf("wrong upgrade error: %v", err)
			}
			if status == 429 && upgrade.Limit != 5 {
				t.Fatalf("connection limit: %v", err)
			}
			assertTokenRedacted(t, err)
		})
	}
	client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("network failure for %s (token: %s)", r.URL, r.Header.Get("X-Colab-Runtime-Proxy-Token"))
	})}
	_, err := colab.DialSSH(t.Context(), &colab.Connection{URL: "https://localhost", Token: testProxyToken}, key, colab.SSHOptions{HTTPClient: client})
	assertTokenRedacted(t, err)
}

func TestSSHUpgradeDoesNotRedirect(t *testing.T) {
	key := sshKey(t)
	var redirected atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer dest.Close()
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer proxy.Close()
	client := proxy.Client()
	_, err := colab.DialSSH(t.Context(), &colab.Connection{URL: proxy.URL, Token: testProxyToken}, key, colab.SSHOptions{HTTPClient: client})
	var upgrade *colab.SSHError
	if !errors.As(err, &upgrade) || upgrade.Status != http.StatusTemporaryRedirect || redirected.Load() != 0 {
		t.Fatalf("unexpected redirect result: %v, %d destination requests", err, redirected.Load())
	}
	assertTokenRedacted(t, err)
}

func TestSSHChannelDialErrors(t *testing.T) {
	for _, channel := range []string{"session", "direct-tcpip"} {
		t.Run(channel, func(t *testing.T) {
			user, host := sshKey(t), sshKey(t)
			pending := make(chan ssh.NewChannel, 1)
			serverDone := make(chan struct{})
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer ws.CloseNow()
				cfg := &ssh.ServerConfig{NoClientAuth: true}
				cfg.AddHostKey(host)
				conn, channels, requests, err := ssh.NewServerConn(websocket.NetConn(r.Context(), ws, websocket.MessageBinary), cfg)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				ch := <-channels
				if ch == nil {
					return
				}
				ch.Reject(ssh.Prohibited, "fixture refused: "+testProxyToken)
				for ch := range channels {
					pending <- ch // Keep the next request unanswered until disconnect.
				}
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			client, err := colab.DialSSH(ctx, &colab.Connection{URL: srv.URL, Token: testProxyToken}, user, colab.SSHOptions{HTTPClient: srv.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			open := func(ctx context.Context) error {
				if channel == "session" {
					_, _, err := client.Run(ctx, "true")
					return err
				}
				conn, err := client.DialPort(ctx, 8888)
				if conn != nil {
					conn.Close()
				}
				return err
			}
			err = open(ctx)
			var rejected *ssh.OpenChannelError
			if !errors.As(err, &rejected) || rejected.Reason != ssh.Prohibited || rejected.Message != "fixture refused: [redacted]" || !colab.Alive(client) {
				t.Fatalf("channel rejection: %v, connection alive: %t", err, colab.Alive(client))
			}
			dialCtx, stop := context.WithTimeout(ctx, 500*time.Millisecond)
			defer stop()
			result := make(chan error, 1)
			go func() { result <- open(dialCtx) }()
			select {
			case ch := <-pending:
				if ch.ChannelType() != channel {
					t.Fatalf("opened %q, want %q", ch.ChannelType(), channel)
				}
			case <-ctx.Done():
				t.Fatal("no channel open reached the server")
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, colab.ErrSSHClosed) || colab.Alive(client) {
					t.Fatalf("stalled dial: %v, connection alive: %t", err, colab.Alive(client))
				}
			case <-ctx.Done():
				t.Fatal("stalled channel dial did not return")
			}
			select {
			case <-serverDone:
			case <-ctx.Done():
				t.Fatal("stalled channel left the server connected")
			}
		})
	}
}

func assertTokenRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testProxyToken) || strings.Contains(err.Error(), url.QueryEscape(testProxyToken)) {
		t.Fatal("SSH error exposed a proxy token")
	}
}
