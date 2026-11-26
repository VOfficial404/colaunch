package agent

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"colaunch/internal/colab"
)

// shareScript runs cloudflared's quick tunnel to a port of the runtime,
// downloading cloudflared, checked against its sha256, the first time.
//
//go:embed share.sh
var shareScript string

// cloudflaredScript has the functions that fetch cloudflared, checked
// against its sha256, which share.sh and domain.sh call.
//
//go:embed cloudflared.sh
var cloudflaredScript string

// SharePrefix starts the IDs of the plugins that run public links.
const SharePrefix = "share-"

// ShareID is the plugin that runs the public link to a port: its pid and
// log are in its folder, which only its owner opens.
func ShareID(port int) string { return fmt.Sprintf("%s%d", SharePrefix, port) }

// How long a public link may take: to download cloudflared, then, once it
// runs, to print its address.
var (
	shareDownload = 5 * time.Minute
	shareTimeout  = 60 * time.Second
	sharePoll     = 2 * time.Second
)

// ShareScript is the script that runs the public link to port, which
// marks its lines in the log with mark.
func ShareScript(port int, mark string) string {
	return fmt.Sprintf("S_PORT=%d\nS_MARK=%s\n", port, shellQuote(mark)) + unix(cloudflaredScript) + unix(shareScript)
}

// unix gives a script Unix line ends, as bash wants.
func unix(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

var (
	quickURL = regexp.MustCompile(`https://([a-z0-9-]+)\.trycloudflare\.com\b`)
	errLine  = regexp.MustCompile(`(?m)^ERROR: (.*)$`)
)

// ShareState is what a public link's log says: whether cloudflared runs,
// its address once it printed it, and why it stopped, if it did.
type ShareState struct {
	Running bool
	URL     string
	Err     string
}

// ParseShare reads the log of a public link's run marked mark, ignoring
// what runs before it wrote.
func ParseShare(log, mark string) ShareState {
	var st ShareState
	i := strings.LastIndex(log, "@@SHARE "+mark+"@@")
	if i < 0 {
		return st
	}
	log = log[i:]
	if m := errLine.FindStringSubmatch(log); m != nil {
		st.Err = strings.TrimSpace(m[1])
		return st
	}
	j := strings.Index(log, "@@SHARE-RUN "+mark+"@@")
	if j < 0 {
		return st
	}
	st.Running = true
	// api.trycloudflare.com is where cloudflared asks for the tunnel, and
	// shows in its errors.
	for _, m := range quickURL.FindAllStringSubmatch(log[j:], -1) {
		if m[1] != "api" {
			st.URL = m[0]
			break
		}
	}
	return st
}

// OpenShare makes port reachable from the internet through a Cloudflare
// quick tunnel, and returns its https://….trycloudflare.com address.
func OpenShare(ctx context.Context, sh *colab.Shell, port int) (string, error) {
	b := make([]byte, 8)
	rand.Read(b)
	mark := hex.EncodeToString(b)
	id := ShareID(port)
	if err := RunPlugin(ctx, sh, id, ShareScript(port, mark)); err != nil {
		return "", err
	}
	url, err := waitShare(ctx, func(ctx context.Context) (string, error) { return Logs(ctx, sh, id, 200) }, mark)
	if err != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		StopPlugin(sctx, sh, id)
		cancel()
		return "", err
	}
	return url, nil
}

// waitShare reads the log until it has the address: cloudflared has
// shareDownload to start, then shareTimeout to print it.
func waitShare(ctx context.Context, logs func(context.Context) (string, error), mark string) (string, error) {
	deadline := time.Now().Add(shareDownload)
	running := false
	for {
		out, err := logs(ctx)
		if err != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		st := ParseShare(out, mark)
		if st.Err != "" {
			return "", errors.New("public link: " + st.Err)
		}
		if st.URL != "" {
			return st.URL, nil
		}
		if st.Running && !running {
			running = true
			deadline = time.Now().Add(shareTimeout)
		}
		if time.Now().After(deadline) {
			if running {
				return "", fmt.Errorf("public link: cloudflared printed no address in %s", shareTimeout)
			}
			return "", fmt.Errorf("public link: cloudflared didn't start in %s", shareDownload)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(sharePoll):
		}
	}
}

// StopShare closes the public link to port.
func StopShare(ctx context.Context, sh *colab.Shell, port int) error {
	return StopPlugin(ctx, sh, ShareID(port))
}
