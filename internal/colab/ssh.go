package colab

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"colaunch/internal/i18n"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

const (
	sshPath           = "/colab/ssh"
	sshPubkeyHeader   = "X-Colab-Ssh-Pubkey"
	sshClientAgent    = "colab-cli"
	sshMode           = "proxy"
	maxSSHSessions    = 5
	sshChannelTimeout = 15 * time.Second
)

var (
	ErrSSHClosed  = transportError("transport.ssh_closed")
	ErrSSHHostKey = transportError("transport.ssh_host_key")
)

type transportError string

func (e transportError) Error() string { return i18n.T(string(e)) }

// SSHError reports a refused WebSocket upgrade. It retains no response
// body or request URL, since either can contain the runtime proxy token.
type SSHError struct {
	Status int
	Limit  int // Any reported connection limit, for HTTP 429.
}

// Fallback reports that the runtime has no SSH upgrade to use.
func (e *SSHError) Fallback() bool {
	switch e.Status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func (e *SSHError) Error() string {
	switch e.Status {
	case http.StatusBadRequest:
		return i18n.F("transport.ssh_key_rejected", e.Status)
	case http.StatusUnauthorized:
		return i18n.F("transport.ssh_token", e.Status)
	case http.StatusForbidden:
		return i18n.F("transport.ssh_forbidden", e.Status)
	case http.StatusNotFound:
		return i18n.F("transport.ssh_missing", e.Status)
	case http.StatusTooManyRequests:
		if e.Limit > 0 {
			return i18n.F("transport.ssh_at_limit", e.Status, e.Limit, e.Limit)
		}
		return i18n.F("transport.ssh_limit", e.Status)
	case http.StatusBadGateway:
		return i18n.F("transport.ssh_unreachable", e.Status)
	default:
		return i18n.F("transport.ssh_upgrade", e.Status)
	}
}

func (e *SSHError) Is(target error) bool {
	return target == ErrUnauthorized && e.Status == http.StatusUnauthorized
}

func sshSessionLimit(body io.Reader) int {
	var response struct {
		Error string `json:"error"`
		Limit int    `json:"limit"`
	}
	if json.NewDecoder(io.LimitReader(body, 1024)).Decode(&response) != nil || response.Error != "already-active-session" || response.Limit < 1 || response.Limit > maxSSHSessions {
		return 0
	}
	return response.Limit
}

// NewSSHKey generates an Ed25519 key as an OpenSSH private key in PEM.
func NewSSHKey() ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "colaunch")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(block), nil
}

// ParseSSHKey reads a key NewSSHKey made without exposing its contents in errors.
func ParseSSHKey(key []byte) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("colab: the stored SSH key is unreadable or is not Ed25519")
	}
	return signer, nil
}

// SSHOptions controls the connection to the runtime proxy.
type SSHOptions struct {
	// HostKey pins the key seen on a previous connection to this runtime.
	HostKey    ssh.PublicKey
	HTTPClient *http.Client
}

// SSH carries commands, uploads, live streams and port forwards on one
// connection, with a separate SSH channel for each operation.
type SSH struct {
	client  *ssh.Client
	hostKey ssh.PublicKey
	token   string
	done    chan struct{}
	once    sync.Once
}

var _ Transport = (*SSH)(nil)

func sshURL(conn *Connection) (string, error) {
	u, err := proxyWebSocketURL(conn, sshPath)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"colab-runtime-proxy-token": {conn.Token},
		"colab-client-agent":        {sshClientAgent, "ssh-mode-" + sshMode},
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// safeSSHError retains cancellation and timeout information while removing
// the token and discarding the original error, which may retain a URL.
func safeSSHError(err error, token string) error {
	for _, ctxErr := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, ctxErr) {
			return ctxErr
		}
	}
	return errors.New(redactSSHToken(err.Error(), token))
}

func redactSSHToken(msg, token string) string {
	if token != "" {
		for _, value := range []string{token, url.QueryEscape(token), url.PathEscape(token)} {
			msg = strings.ReplaceAll(msg, value, "[redacted]")
		}
	}
	return msg
}

func sshStageError(err error, token, key string) error {
	err = safeSSHError(err, token)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New(i18n.F(key, err))
}

// DialSSH uses the same upgrade and byte stream as colab-cli's ProxyCommand.
// TLS authenticates the Colab proxy. The SSH host key presented through it
// is trusted on first use and checked on later connections to the runtime.
func DialSSH(ctx context.Context, conn *Connection, signer ssh.Signer, opt SSHOptions) (*SSH, error) {
	u, err := sshURL(conn)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set(sshPubkeyHeader, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))))
	h.Set(clientAgentHeader, sshClientAgent)
	h.Set(proxyTokenHeader, conn.Token)
	ws, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h, HTTPClient: proxyWebSocketClient(opt.HTTPClient)})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if resp != nil {
			e := &SSHError{Status: resp.StatusCode}
			if e.Status == http.StatusTooManyRequests && resp.Body != nil {
				e.Limit = sshSessionLimit(resp.Body)
			}
			return nil, e
		}
		return nil, sshStageError(proxyWebSocketError(ctx, err), conn.Token, "transport.upgrade_failed")
	}
	nc := &sshWebSocket{Conn: websocket.NetConn(context.Background(), ws, websocket.MessageBinary), ws: ws}
	ws.SetReadLimit(8 << 20)
	seen := opt.HostKey
	cfg := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if seen == nil {
				seen = key
			} else if !bytes.Equal(seen.Marshal(), key.Marshal()) {
				return ErrSSHHostKey
			}
			return nil
		},
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	c, chans, reqs, err := ssh.NewClientConn(nc, "runtime:22", cfg)
	stop()
	if ctx.Err() != nil {
		nc.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, ErrSSHHostKey) {
			return nil, ErrSSHHostKey
		}
		return nil, sshStageError(err, conn.Token, "transport.handshake_failed")
	}
	s := &SSH{client: ssh.NewClient(c, chans, reqs), hostKey: seen, token: conn.Token, done: make(chan struct{})}
	go func() {
		s.client.Wait()
		s.Close()
	}()
	go s.keepAlive()
	return s, nil
}

// HostKey is the runtime's SSH host key for use on reconnect.
func (s *SSH) HostKey() ssh.PublicKey { return s.hostKey }

func (s *SSH) keepAlive() {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
		}
		answered := make(chan error, 1)
		go func() {
			// A server that rejects the request still proves it is alive.
			_, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil)
			answered <- err
		}()
		select {
		case err := <-answered:
			if err == nil {
				continue
			}
		case <-time.After(15 * time.Second):
		case <-s.done:
			return
		}
		s.Close()
		return
	}
}

// Close closes the connection and its channels.
func (s *SSH) Close() error {
	s.once.Do(func() {
		s.client.Close()
		close(s.done)
	})
	return nil
}

// Done is closed once the connection is closed.
func (s *SSH) Done() <-chan struct{} { return s.done }

func (s *SSH) operationError(err error) error {
	err = safeSSHError(err, s.token)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	select {
	case <-s.done:
		return fmt.Errorf("%w: %w", ErrSSHClosed, err)
	default:
		return err
	}
}

// An open request cannot be canceled once sent. Close the underlying SSH
// connection if the peer has not answered by the deadline to unblock the dial.
func openSSHChannel[T any](ctx context.Context, s *SSH, open func() (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, sshChannelTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	select {
	case <-s.done:
		return zero, ErrSSHClosed
	default:
	}
	type result struct {
		channel T
		err     error
	}
	opened := make(chan result, 1)
	go func() {
		ch, err := open()
		opened <- result{ch, err}
	}()
	select {
	case <-ctx.Done():
		s.Close()
		return zero, ctx.Err()
	case r := <-opened:
		if r.err == nil {
			return r.channel, nil
		}
		var rejected *ssh.OpenChannelError
		if errors.As(r.err, &rejected) {
			return zero, &ssh.OpenChannelError{Reason: rejected.Reason, Message: redactSSHToken(rejected.Message, s.token)}
		}
		s.Close()
		return zero, s.operationError(r.err)
	}
}

// Match colab-cli's working directory, falling back to the login home.
const sshPrelude = `cd /content 2>/dev/null; `

func (s *SSH) exec(ctx context.Context, command string, stdin io.Reader) (string, int, error) {
	sess, err := openSSHChannel(ctx, s, s.client.NewSession)
	if err != nil {
		return "", -1, err
	}
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	sess.Stdin = stdin
	go func() {
		out, err := sess.CombinedOutput(command)
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		sess.Close()
		var exit *ssh.ExitError
		switch {
		case r.err == nil:
			return string(r.out), 0, nil
		case errors.As(r.err, &exit):
			return string(r.out), exit.ExitStatus(), nil
		default:
			return "", -1, s.operationError(r.err)
		}
	case <-ctx.Done():
		go sess.Close()
		return "", -1, ctx.Err()
	}
}

// Run runs a bash command and returns its combined output and exit status.
func (s *SSH) Run(ctx context.Context, cmd string) (string, int, error) {
	return s.exec(ctx, "bash -c "+shellQuote(sshPrelude+cmd)+" 2>&1", nil)
}

// Upload streams to a private file in the destination directory, then
// atomically replaces the destination. Verify the length first because a
// dropped SSH channel can look like EOF to cat.
func (s *SSH) Upload(ctx context.Context, path string, data []byte) error {
	script := fmt.Sprintf(`umask 077; mkdir -p -- %s || exit 1
dst=%s
part=$(mktemp -- "$dst.colaunch-XXXXXXXX") || exit 1
trap 'rm -f -- "$part"' EXIT
cat > "$part" && [ "$(wc -c < "$part")" -eq %d ] && [ ! -d "$dst" ] && mv -f -- "$part" "$dst"`,
		shellQuote(pathpkg.Dir(path)), shellQuote(path), len(data))
	_, code, err := s.exec(ctx, "bash -c "+shellQuote(sshPrelude+script), bytes.NewReader(data))
	if err == nil && code != 0 {
		err = safeSSHError(errors.New(i18n.F("transport.upload_failed", path, code)), s.token)
	}
	return err
}

// SSHStream is the input and output of a command running without a PTY.
type SSHStream struct {
	sess  *ssh.Session
	stdin io.Writer
	done  chan struct{}
	once  sync.Once
	token string
}

// Stream starts a command and waits until it prints the ready marker.
// The context bounds startup. The caller owns the returned stream.
func (s *SSH) Stream(ctx context.Context, command, ready string) (*SSHStream, *bufio.Reader, error) {
	sess, err := openSSHChannel(ctx, s, s.client.NewSession)
	if err != nil {
		return nil, nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, nil, s.operationError(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, nil, s.operationError(err)
	}
	st := &SSHStream{sess: sess, stdin: stdin, done: make(chan struct{}), token: s.token}
	r := bufio.NewReaderSize(stdout, 256<<10)
	found := make(chan error, 1)
	go func() {
		err := sess.Start("bash -c " + shellQuote(sshPrelude+"exec "+command))
		if err != nil {
			found <- err
			st.Close()
			return
		}
		go func() {
			sess.Wait()
			st.Close()
		}()
		for {
			line, err := r.ReadSlice('\n')
			if err != nil || isReadyLine(string(line), ready) {
				found <- err
				return
			}
		}
	}()
	select {
	case err := <-found:
		if err != nil {
			st.Close()
			return nil, nil, s.operationError(err)
		}
		return st, r, nil
	case <-ctx.Done():
		st.Close()
		return nil, nil, ctx.Err()
	}
}

// WriteStringContext writes to stdin. A timeout closes the channel.
func (st *SSHStream) WriteStringContext(ctx context.Context, data string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wrote := make(chan error, 1)
	go func() {
		_, err := io.WriteString(st.stdin, data)
		wrote <- err
	}()
	select {
	case err := <-wrote:
		if err != nil {
			return safeSSHError(err, st.token)
		}
		return nil
	case <-ctx.Done():
		st.Close()
		return ctx.Err()
	case <-st.done:
		return io.ErrClosedPipe
	}
}

// Close ends the command's SSH channel.
func (st *SSHStream) Close() error {
	st.once.Do(func() {
		close(st.done)
		go st.sess.Close()
	})
	return nil
}

// DialPort opens a direct-tcpip channel to the runtime's loopback address.
func (s *SSH) DialPort(ctx context.Context, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New(i18n.T("transport.port_invalid"))
	}
	return openSSHChannel(ctx, s, func() (net.Conn, error) {
		return s.client.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	})
}

// NetConn sends binary frames. Override its reader to accept text frames
// as well, matching the official CLI's bridge.
type sshWebSocket struct {
	net.Conn
	ws *websocket.Conn
	r  io.Reader
}

func (c *sshWebSocket) Read(p []byte) (int, error) {
	for {
		if c.r == nil {
			_, r, err := c.ws.Reader(context.Background())
			if err != nil {
				return 0, err
			}
			c.r = r
		}
		n, err := c.r.Read(p)
		if errors.Is(err, io.EOF) {
			c.r = nil
			if n == 0 {
				continue
			}
			return n, nil
		}
		return n, err
	}
}

func (c *sshWebSocket) Close() error {
	err := c.ws.CloseNow()
	c.Conn.Close()
	return err
}
