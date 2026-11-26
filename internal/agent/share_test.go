package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A log as cloudflared 2025.x writes it to stderr, after the script's
// lines, with a failed first request to api.trycloudflare.com.
const cloudflaredLog = `@@SHARE old@@
@@SHARE-RUN old@@
2025-03-01T09:12:01Z INF |  https://stale-old-tunnel-here.trycloudflare.com                                  |
@@SHARE m1@@
Looking up cloudflared's latest release
Downloading https://github.com/cloudflare/cloudflared/releases/download/2025.8.1/cloudflared-linux-amd64
Checksum verified: sha256 2bd6bd8e40bd8e1aa3e9fe2cf1e1e8ddcb1a7e1c3ef0b65d4b2c7d6b0e2a9f11
cloudflared version 2025.8.1 (built 2025-08-21-1504 UTC)
@@SHARE-RUN m1@@
2025-08-25T03:14:07Z INF Thank you for trying Cloudflare Tunnel. Doing so, without a Cloudflare account, is a quick way to experiment and try it out. However, be aware that these account-less Tunnels have no uptime guarantee, are subject to the Cloudflare Online Services Terms of Use (https://www.cloudflare.com/website-terms/), and Cloudflare reserves the right to investigate your use of Tunnels for violations of such terms. If you intend to use Tunnels in production you should use a pre-created named tunnel by following: https://developers.cloudflare.com/cloudflare-one/connections/connect-apps
2025-08-25T03:14:07Z INF Requesting new quick Tunnel on trycloudflare.com...
2025-08-25T03:14:08Z ERR Error unmarshaling QuickTunnel response: error="failed to request quick Tunnel: Post \"https://api.trycloudflare.com/tunnel\": context deadline exceeded"
2025-08-25T03:14:09Z INF +--------------------------------------------------------------------------------------------+
2025-08-25T03:14:09Z INF |  Your quick Tunnel has been created! Visit it at (it may take some time to be reachable):  |
2025-08-25T03:14:09Z INF |  https://seasonal-deck-organisms-sf.trycloudflare.com                                      |
2025-08-25T03:14:09Z INF +--------------------------------------------------------------------------------------------+
2025-08-25T03:14:09Z INF Cannot determine default configuration path. No file [config.yml config.yaml] in [~/.cloudflared ~/.cloudflare-warp ~/cloudflare-warp /etc/cloudflared /usr/local/etc/cloudflared]
2025-08-25T03:14:09Z INF Version 2025.8.1 (Checksum 2bd6bd8e40bd8e1aa3e9fe2cf1e1e8ddcb1a7e1c3ef0b65d4b2c7d6b0e2a9f11)
2025-08-25T03:14:10Z INF Registered tunnel connection connIndex=0 connection=3e1c6f2a event=0 ip=198.41.200.13 location=sin07 protocol=quic
`

// The address is the quick tunnel's of this run, not one of a run before,
// nor the API cloudflared asks.
func TestParseShare(t *testing.T) {
	st := ParseShare(cloudflaredLog, "m1")
	if !st.Running || st.URL != "https://seasonal-deck-organisms-sf.trycloudflare.com" || st.Err != "" {
		t.Errorf("parsed %+v", st)
	}
	if st := ParseShare(cloudflaredLog, "m2"); st.Running || st.URL != "" {
		t.Errorf("a run that hasn't written yet: %+v", st)
	}
	starting := cloudflaredLog[:strings.Index(cloudflaredLog, "2025-08-25T03:14:09Z")]
	if st := ParseShare(starting, "m1"); !st.Running || st.URL != "" {
		t.Errorf("before the address: %+v", st)
	}
	failed := "@@SHARE m3@@\nLooking up cloudflared's latest release\nERROR: GitHub gives no sha256 for cloudflared-linux-amd64: not running it unverified\n"
	if st := ParseShare(failed, "m3"); st.Err != "GitHub gives no sha256 for cloudflared-linux-amd64: not running it unverified" || st.Running {
		t.Errorf("failed: %+v", st)
	}
}

// Waiting gives cloudflared shareTimeout to print its address once it
// runs, and stops at the script's error.
func TestWaitShare(t *testing.T) {
	oldPoll, oldTimeout := sharePoll, shareTimeout
	sharePoll, shareTimeout = time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { sharePoll, shareTimeout = oldPoll, oldTimeout })
	ctx := context.Background()
	calls := 0
	url, err := waitShare(ctx, func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "@@SHARE m1@@\n", nil
		}
		return cloudflaredLog, nil
	}, "m1")
	if err != nil || url != "https://seasonal-deck-organisms-sf.trycloudflare.com" {
		t.Errorf("url %q, %v", url, err)
	}
	_, err = waitShare(ctx, func(context.Context) (string, error) {
		return "@@SHARE m1@@\n@@SHARE-RUN m1@@\n2025-08-25T03:14:07Z INF Requesting new quick Tunnel on trycloudflare.com...\n", nil
	}, "m1")
	if err == nil || !strings.Contains(err.Error(), "no address") {
		t.Errorf("no address: %v", err)
	}
	_, err = waitShare(ctx, func(context.Context) (string, error) {
		return "@@SHARE m1@@\nERROR: checksum mismatch\n", nil
	}, "m1")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("script error: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := waitShare(cctx, func(context.Context) (string, error) { return "", context.Canceled }, "m1"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}

// The script gets its port and mark as plain shell assignments, with
// Unix line ends.
func TestShareScript(t *testing.T) {
	s := ShareScript(8188, "abc")
	if !strings.HasPrefix(s, "S_PORT=8188\nS_MARK='abc'\n") || strings.Contains(s, "\r") {
		t.Errorf("script starts %q", s[:40])
	}
	if !strings.Contains(s, "--no-autoupdate --url \"http://127.0.0.1:$S_PORT\"") || !strings.Contains(s, "digest") {
		t.Error("the script doesn't run the tunnel as expected, or doesn't check the digest")
	}
	if ShareID(8188) != "share-8188" {
		t.Error(ShareID(8188))
	}
}
