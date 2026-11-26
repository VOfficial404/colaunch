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

// domainScript runs a named Cloudflare Tunnel with a token kept in a
// private file, downloading cloudflared, checked against its sha256, the
// first time.
//
//go:embed domain.sh
var domainScript string

// DomainPrefix starts the IDs of the plugins that run links on the user's
// own domain.
const DomainPrefix = "domain-"

var configID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,32}$`)

// DomainID is the plugin that runs the tunnel of a domain configuration:
// one at a time, as a configuration serves one port at a time.
func DomainID(config string) string { return DomainPrefix + config }

// How long a domain's tunnel may take: to download cloudflared, then, once
// it runs, to register a connection with Cloudflare.
var (
	domainDownload = 5 * time.Minute
	domainTimeout  = 60 * time.Second
	domainPoll     = 2 * time.Second
)

// DomainScript is the script that runs the tunnel whose token is in
// tokenFile, which marks its lines in the log with mark.
func DomainScript(tokenFile, mark string) string {
	return fmt.Sprintf("D_TOKEN_FILE=%s\nD_MARK=%s\n", shellQuote(tokenFile), shellQuote(mark)) + unix(cloudflaredScript) + unix(domainScript)
}

// cloudflared logs this, at level INF, for each connection to Cloudflare's
// edge once registered (connection/observer.go, logConnected), and this
// when the token can't be parsed (cmd/cloudflared/tunnel/subcommands.go).
const (
	registeredLine = "Registered tunnel connection"
	badTokenLine   = "Provided Tunnel token is not valid"
)

// DomainState is what a domain tunnel's log says: whether cloudflared runs,
// whether it has registered a connection, and why it stopped, if it did.
type DomainState struct {
	Running   bool
	Connected bool
	Err       string
}

// ParseDomain reads the log of a domain tunnel's run marked mark, ignoring
// what runs before it wrote.
func ParseDomain(log, mark string) DomainState {
	var st DomainState
	i := strings.LastIndex(log, "@@DOMAIN "+mark+"@@")
	if i < 0 {
		return st
	}
	log = log[i:]
	j := strings.Index(log, "@@DOMAIN-RUN "+mark+"@@")
	if j >= 0 {
		run := log[j:]
		st.Running = true
		st.Connected = strings.Contains(run, registeredLine)
		if strings.Contains(run, badTokenLine) {
			st.Err = "Cloudflare doesn't accept the tunnel token: copy it again from the tunnel's page in Cloudflare Zero Trust"
			st.Running, st.Connected = false, false
			return st
		}
	}
	if m := errLine.FindStringSubmatch(log); m != nil {
		st.Err = strings.TrimSpace(m[1])
		st.Running, st.Connected = false, false
	}
	return st
}

// OpenDomain runs the tunnel of the domain configuration config on the
// runtime with token, and returns once it has registered a connection with
// Cloudflare. The token goes to a file of the plugin's private folder,
// readable by its owner alone; stopping the plugin removes it.
func OpenDomain(ctx context.Context, sh *colab.Shell, config, token string) error {
	if !configID.MatchString(config) {
		return fmt.Errorf("domain: bad configuration ID %q", config)
	}
	if strings.ContainsAny(token, " \t\r\n") || token == "" {
		return errors.New("domain: the tunnel token is empty, or has spaces in it")
	}
	b := make([]byte, 8)
	rand.Read(b)
	mark := hex.EncodeToString(b)
	id := DomainID(config)
	// What's left of a run before, its old token with it.
	if err := StopPlugin(ctx, sh, id); err != nil {
		return err
	}
	path, err := PutPrivate(ctx, sh, id, "tunnel-token", []byte(token))
	if err != nil {
		return err
	}
	if err := RunPlugin(ctx, sh, id, DomainScript(path, mark)); err != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		StopPlugin(sctx, sh, id)
		cancel()
		return err
	}
	if err := waitDomain(ctx, func(ctx context.Context) (string, error) { return Logs(ctx, sh, id, 200) }, mark); err != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		StopPlugin(sctx, sh, id)
		cancel()
		return err
	}
	return nil
}

// waitDomain reads the log until cloudflared has registered a connection:
// it has domainDownload to start, then domainTimeout to connect.
func waitDomain(ctx context.Context, logs func(context.Context) (string, error), mark string) error {
	deadline := time.Now().Add(domainDownload)
	running := false
	for {
		out, err := logs(ctx)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		st := ParseDomain(out, mark)
		if st.Err != "" {
			return errors.New("domain: " + st.Err)
		}
		if st.Connected {
			return nil
		}
		if st.Running && !running {
			running = true
			deadline = time.Now().Add(domainTimeout)
		}
		if time.Now().After(deadline) {
			if running {
				return fmt.Errorf("domain: cloudflared registered no connection with Cloudflare in %s: check the tunnel token, and that the runtime can reach Cloudflare", domainTimeout)
			}
			return fmt.Errorf("domain: cloudflared didn't start in %s", domainDownload)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(domainPoll):
		}
	}
}

// StopDomain stops the tunnel of a domain configuration, and removes its
// token from the runtime.
func StopDomain(ctx context.Context, sh *colab.Shell, config string) error {
	return StopPlugin(ctx, sh, DomainID(config))
}
