package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A named tunnel's log as cloudflared 2025.x writes it, after the script's
// lines, with a run before it.
const domainLog = `@@DOMAIN old@@
@@DOMAIN-RUN old@@
2025-08-25T03:10:10Z INF Registered tunnel connection connIndex=0 connection=aaaa event=0 ip=198.41.200.13 location=sin07 protocol=quic
@@DOMAIN m1@@
cloudflared version 2025.8.1 (built 2025-08-21-1504 UTC)
@@DOMAIN-RUN m1@@
2025-08-25T03:14:07Z INF Starting tunnel tunnelID=4f8b1c3e-0d1a-4c3b-9d6e-2a7f5b8c9d01
2025-08-25T03:14:07Z INF Version 2025.8.1 (Checksum 2bd6bd8e40bd8e1aa3e9fe2cf1e1e8ddcb1a7e1c3ef0b65d4b2c7d6b0e2a9f11)
2025-08-25T03:14:07Z INF Generated Connector ID: 0c5d6e7f-1111-2222-3333-444455556666
2025-08-25T03:14:08Z INF Initial protocol quic
2025-08-25T03:14:08Z INF Starting metrics server on 127.0.0.1:20241/metrics
2025-08-25T03:14:09Z INF Registered tunnel connection connIndex=0 connection=3e1c6f2a event=0 ip=198.41.200.13 location=sin07 protocol=quic
`

// Connected is this run's registered connection, not one of a run before.
func TestParseDomain(t *testing.T) {
	if st := ParseDomain(domainLog, "m1"); !st.Running || !st.Connected || st.Err != "" {
		t.Errorf("parsed %+v", st)
	}
	starting := domainLog[:strings.Index(domainLog, "2025-08-25T03:14:09Z")]
	if st := ParseDomain(starting, "m1"); !st.Running || st.Connected {
		t.Errorf("before connecting: %+v", st)
	}
	if st := ParseDomain(domainLog, "m2"); st.Running || st.Connected {
		t.Errorf("a run that hasn't written: %+v", st)
	}
	bad := "@@DOMAIN m3@@\n@@DOMAIN-RUN m3@@\n2025-08-25T03:14:07Z ERR Provided Tunnel token is not valid.\nERROR: cloudflared stopped (exit status 1)\n"
	if st := ParseDomain(bad, "m3"); st.Connected || !strings.Contains(st.Err, "tunnel token") {
		t.Errorf("bad token: %+v", st)
	}
	failed := "@@DOMAIN m4@@\nERROR: checksum mismatch for cloudflared-linux-amd64\n"
	if st := ParseDomain(failed, "m4"); st.Running || st.Err != "checksum mismatch for cloudflared-linux-amd64" {
		t.Errorf("failed: %+v", st)
	}
}

// Waiting gives cloudflared domainTimeout to register once it runs.
func TestWaitDomain(t *testing.T) {
	oldPoll, oldTimeout := domainPoll, domainTimeout
	domainPoll, domainTimeout = time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { domainPoll, domainTimeout = oldPoll, oldTimeout })
	ctx := context.Background()
	calls := 0
	err := waitDomain(ctx, func(context.Context) (string, error) {
		if calls++; calls < 3 {
			return "@@DOMAIN m1@@\n", nil
		}
		return domainLog, nil
	}, "m1")
	if err != nil {
		t.Errorf("connected: %v", err)
	}
	err = waitDomain(ctx, func(context.Context) (string, error) {
		return "@@DOMAIN m1@@\n@@DOMAIN-RUN m1@@\n2025-08-25T03:14:07Z INF Starting tunnel\n", nil
	}, "m1")
	if err == nil || !strings.Contains(err.Error(), "registered no connection") {
		t.Errorf("never connected: %v", err)
	}
	err = waitDomain(ctx, func(context.Context) (string, error) {
		return "@@DOMAIN m1@@\n@@DOMAIN-RUN m1@@\n2025-08-25T03:14:07Z ERR Provided Tunnel token is not valid.\n", nil
	}, "m1")
	if err == nil || !strings.Contains(err.Error(), "tunnel token") {
		t.Errorf("bad token: %v", err)
	}
}

// The script gets the token's path, never the token, and runs cloudflared
// with --token-file.
func TestDomainScript(t *testing.T) {
	dir := "/content/colaunch/plugins/domain-x/private/tmp1"
	s := DomainScript(dir+"/f", "abc")
	if !strings.HasPrefix(s, "D_TOKEN_FILE='"+dir+"/f'\nD_MARK='abc'\n") || strings.Contains(s, "\r") {
		t.Errorf("script starts %q", s[:80])
	}
	if !strings.Contains(s, `run --token-file "$D_TOKEN_FILE"`) || !strings.Contains(s, "digest") || strings.Contains(s, "--token \"") {
		t.Error("the script doesn't run the tunnel from the token's file, or doesn't check the digest")
	}
	if DomainID("abc123") != "domain-abc123" {
		t.Error(DomainID("abc123"))
	}
}

func bashOrSkip(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	if out, err := exec.Command(bash, "-c", "true").CombinedOutput(); err != nil {
		t.Skipf("bash doesn't run here: %v %s", err, out)
	}
	return bash
}

// The runtime's scripts are bash that parses.
func TestScriptsParse(t *testing.T) {
	bash := bashOrSkip(t)
	for name, s := range map[string]string{"share": ShareScript(8188, "m"), "domain": DomainScript("/x/private/t", "m")} {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n: %v\n%s", name, err, out)
		}
	}
}

// domain.sh runs a cloudflared that is there, with the token from its
// file and not on the command line, prints the marks, and removes the
// token's file when cloudflared ends. A fake cloudflared stands in.
func TestDomainScriptRuns(t *testing.T) {
	bash := bashOrSkip(t)
	if out, _ := exec.Command(bash, "-c", "uname -m").Output(); strings.TrimSpace(string(out)) != "x86_64" {
		t.Skip("not amd64")
	}
	const token = "fake-tunnel-token-for-tests"
	for _, tokenFile := range []bool{true, false} {
		home := t.TempDir()
		plugin := filepath.Join(home, "plugins", "domain-c1")
		priv := filepath.Join(plugin, "private", "tmp1")
		os.MkdirAll(priv, 0o700)
		os.MkdirAll(filepath.Join(home, "cloudflared"), 0o700)
		tf := filepath.Join(priv, "tunnel-token")
		os.WriteFile(tf, []byte(token), 0o600)
		args := filepath.Join(home, "args.txt")
		help := "   --token-file value  Filepath at which to read the tunnel token"
		if !tokenFile {
			help = "   --token value  The Tunnel token"
		}
		fake := `#!/usr/bin/env bash
case "$*" in
  --version) echo "cloudflared version 2025.8.1"; exit 0 ;;
  "tunnel run --help") echo "` + help + `"; exit 0 ;;
esac
echo "$*" > "` + filepath.ToSlash(args) + `"
read_token=""
while [ $# -gt 0 ]; do
  if [ "$1" = --token-file ]; then read_token="$(cat "$2")"; fi
  shift
done
[ -n "$read_token" ] || read_token="$TUNNEL_TOKEN"
[ "$read_token" = "` + token + `" ] || { echo "ERR wrong token"; exit 3; }
echo "2025-08-25T03:14:09Z INF Registered tunnel connection connIndex=0 location=sin07 protocol=quic"
`
		os.WriteFile(filepath.Join(home, "cloudflared", "cloudflared"), []byte(fake), 0o755)
		script := filepath.Join(home, "run.sh")
		os.WriteFile(script, []byte(DomainScript(filepath.ToSlash(tf), "m1")), 0o600)
		cmd := exec.Command(bash, filepath.ToSlash(script))
		cmd.Env = append(os.Environ(), "COLAUNCH_HOME="+filepath.ToSlash(home), "COLAUNCH_PLUGIN_DIR="+filepath.ToSlash(plugin))
		out, _ := cmd.CombinedOutput()
		log := string(out)
		st := ParseDomain(log, "m1")
		// cloudflared ended at once, which the script reports.
		if !strings.Contains(log, "@@DOMAIN-RUN m1@@") || !strings.Contains(log, registeredLine) || !strings.Contains(st.Err, "cloudflared stopped") {
			t.Fatalf("token file %v: log:\n%s", tokenFile, log)
		}
		got, _ := os.ReadFile(args)
		if strings.Contains(string(got), token) || strings.Contains(log, token) {
			t.Errorf("the token is on the command line or in the log: %s", got)
		}
		if tokenFile != strings.Contains(string(got), "--token-file") {
			t.Errorf("token file %v: args %s", tokenFile, got)
		}
		if _, err := os.Stat(tf); !os.IsNotExist(err) {
			t.Errorf("the token's file stayed: %v", err)
		}
	}
}

// The token must be in the plugin's private folder.
func TestDomainScriptRefusesOtherFiles(t *testing.T) {
	bash := bashOrSkip(t)
	home := t.TempDir()
	tf := filepath.Join(home, "token")
	os.WriteFile(tf, []byte("x"), 0o600)
	cmd := exec.Command(bash, "-c", DomainScript(filepath.ToSlash(tf), "m1"))
	cmd.Env = append(os.Environ(), "COLAUNCH_HOME="+filepath.ToSlash(home), "COLAUNCH_PLUGIN_DIR="+filepath.ToSlash(filepath.Join(home, "plugins", "domain-c1")))
	out, _ := cmd.CombinedOutput()
	if st := ParseDomain(string(out), "m1"); !strings.Contains(st.Err, "private folder") {
		t.Errorf("ran with a token elsewhere: %s", out)
	}
}
