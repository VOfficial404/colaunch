package colab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// streamTerminal serves /colab/tty as a terminal that, once it reads the
// line starting the command, prints out.
func streamTerminal(t *testing.T, out string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			var msg struct{ Data *string }
			if err := wsjson.Read(r.Context(), ws, &msg); err != nil {
				return
			}
			if msg.Data != nil && strings.HasPrefix(*msg.Data, "stty raw -echo; exec ") {
				if wsjson.Write(r.Context(), ws, map[string]string{"data": out}) != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The command's ready line may follow what an earlier terminal printed
// without ending its line, as an answer cut short; the command's own
// line, echoed as typed, has the marker quoted inside and never counts.
func TestStreamReadyLine(t *testing.T) {
	const ready = "@@COLAUNCH-WAIT-READY-abc@@"
	const command = "python3 -u /content/colaunch/agent.py wait-ready 'p' 16384 '" + ready + "'"
	echo := "stty raw -echo; exec " + command + "\r\n"
	for _, c := range []struct {
		name, out string
		ok        bool
	}{
		{"alone", ready + "\r\n", true},
		{"after stale bytes", "@@WR 0123456789ab 1a2b3c4d log 10 0 yes QUJD" + ready + "\r\n", true},
		{"after the echo", echo + ready + "\n", true},
		{"echo only", echo, false},
		{"echo without its newline yet", strings.TrimSuffix(echo, "\r\n"), false},
		{"followed by more", ready + " more\r\n", false},
		{"another terminal's", "@@COLAUNCH-WAIT-READY-old@@\r\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
			defer cancel()
			srv := streamTerminal(t, c.out)
			tty, _, err := openStream(ctx, &Connection{URL: srv.URL}, "exec "+command, ready, false, srv.Client())
			if tty != nil {
				tty.Close()
			}
			if c.ok && err != nil {
				t.Fatalf("not ready: %v", err)
			}
			if !c.ok && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want it to wait on", err)
			}
		})
	}
}
