package colab

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func proxyWebSocketURL(conn *Connection, endpoint string) (*url.URL, error) {
	invalid := transportError("transport.proxy_invalid")
	if conn == nil {
		return nil, invalid
	}
	u, err := url.Parse(conn.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil, invalid
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		// Older terminal fixtures use plaintext and one sends the fixed dummy
		// token "secret". Never send real credentials over plaintext.
		host := u.Hostname()
		if !testing.Testing() || !(strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()) ||
			(conn.Token != "" && !(endpoint == ttyPath && conn.Token == "secret")) {
			return nil, invalid
		}
		u.Scheme = "ws"
	default:
		return nil, invalid
	}
	if endpoint == ttyPath {
		// The terminal can be served under the proxy's base path.
		escaped := u.EscapedPath()
		if strings.HasSuffix(escaped, "/") {
			u.Path = strings.TrimSuffix(u.Path, "/")
		}
		u.RawPath = strings.TrimSuffix(escaped, "/") + endpoint
		u.Path += endpoint
	} else {
		u.Path, u.RawPath = endpoint, ""
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	u.ForceQuery = false
	return u, nil
}

func proxyWebSocketClient(base *http.Client) *http.Client {
	client := &http.Client{}
	if base != nil {
		*client = *base
	}
	// The credentials and key registration belong to the original proxy.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

func proxyWebSocketError(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil {
		return cause
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			return cause
		}
	}
	// The dialer's error can contain the request query, headers, or an
	// echoed response. Do not expose it through formatting or unwrapping.
	return errors.New("WebSocket handshake failed")
}

// Transport runs commands on a runtime and puts files there. Colaunch
// prefers Colab's SSH endpoint (SSH) and falls back to the terminal
// (Shell) on runtimes without one.
type Transport interface {
	// Run runs a bash command and returns what it printed, stdout and
	// stderr together, and its exit status.
	Run(ctx context.Context, cmd string) (string, int, error)
	// Upload writes data to path on the runtime, making its directory,
	// through a file of its own next to path moved into place once whole.
	Upload(ctx context.Context, path string, data []byte) error
	// Close closes the connection.
	Close() error
	// Done is closed once the connection is.
	Done() <-chan struct{}
}

// Alive reports whether t is open.
func Alive(t Transport) bool {
	if t == nil {
		return false
	}
	select {
	case <-t.Done():
		return false
	default:
		return true
	}
}
