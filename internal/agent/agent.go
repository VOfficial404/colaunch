// Package agent is the side of Colaunch that runs on a runtime: agent.py,
// which the app uploads, and the calls the app makes to it.
package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"colaunch/internal/colab"
)

//go:embed agent.py
var Script []byte

// Home is where the agent keeps its files on the runtime.
const Home = "/content/colaunch"

// Path is where the agent is.
const Path = Home + "/agent.py"

// MuxReady is the line the tunnel prints once it reads.
const MuxReady = "@@COLAUNCH-MUX-READY@@"

// MuxCommand starts the tunnel.
const MuxCommand = "python3 -u " + Path + " mux"

// Install puts the agent on the runtime, in a folder only its owner
// reads.
func Install(ctx context.Context, sh *colab.Shell) error {
	if _, _, err := sh.Run(ctx, "mkdir -p "+Home+" && chmod 700 "+Home); err != nil {
		return err
	}
	return sh.Upload(ctx, Path, Script)
}

// PluginDir is a plugin's folder on the runtime.
func PluginDir(id string) string { return Home + "/plugins/" + id }

// agentCmd runs the agent with args and returns its last line.
func agentCmd(ctx context.Context, sh *colab.Shell, args ...string) (string, error) {
	cmd := "python3 " + Path
	for _, a := range args {
		cmd += " " + shellQuote(a)
	}
	out, code, err := sh.Run(ctx, cmd)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("agent: %s: %s", args[0], strings.TrimSpace(out))
	}
	return lastLine(out), nil
}

// PutPrivate puts data in a new file named name, which only its owner
// reads, in a folder of the plugin's made for it alone, and returns the
// file's path. The plugin removes it once it's read; stopping the plugin
// removes what's left.
func PutPrivate(ctx context.Context, sh *colab.Shell, id, name string, data []byte) (string, error) {
	dir, err := agentCmd(ctx, sh, "private", id)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(dir, PluginDir(id)+"/") {
		return "", fmt.Errorf("agent: private: %q", dir)
	}
	path := dir + "/" + name
	if err := sh.Upload(ctx, path, data); err != nil {
		return "", err
	}
	if _, err := agentCmd(ctx, sh, "seal", path); err != nil {
		return "", err
	}
	return path, nil
}

// Watchdog is when the runtime gives itself back. Times are Unix seconds,
// 0 for none.
type Watchdog struct {
	StopAt      int64   `json:"stop_at"`
	BudgetAt    int64   `json:"budget_at"`
	IdleMinutes float64 `json:"idle_minutes"`
}

// SetWatchdog writes the watchdog's limits and starts it when it isn't
// running: it reads them anew every few seconds.
func SetWatchdog(ctx context.Context, sh *colab.Shell, w Watchdog) error {
	b, _ := json.Marshal(w)
	if err := sh.Upload(ctx, Home+"/watchdog.json", b); err != nil {
		return err
	}
	out, code, err := sh.Run(ctx, "python3 "+Path+" watchdog")
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("agent: watchdog: %s", strings.TrimSpace(out))
	}
	return nil
}

// NoticesPath is where the requests the watchdog makes before it stops the
// runtime are kept, in a folder only its owner opens.
const NoticesPath = Home + "/notices/notices.json"

// Notice is an HTTP request the watchdog makes before it stops the runtime.
// The agent puts why it stops in place of "{reason}" in Body, escaped for
// a JSON string, and the time in place of "{time}".
type Notice struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Notices are what the watchdog sends when it stops the runtime.
type Notices struct {
	// Reasons are the words for why, by the watchdog's reason:
	// "schedule", "idle" or "budget".
	Reasons  map[string]string `json:"reasons"`
	Requests []Notice          `json:"requests"`
}

// SetNotices gives the watchdog the requests to make before it stops the
// runtime, in a file only the runtime's owner reads; none removes it.
func SetNotices(ctx context.Context, sh *colab.Shell, n Notices) error {
	if len(n.Requests) == 0 {
		_, err := agentCmd(ctx, sh, "notices", "clear")
		return err
	}
	dir, err := agentCmd(ctx, sh, "notices", "prepare")
	if err != nil {
		return err
	}
	if dir+"/notices.json" != NoticesPath {
		return fmt.Errorf("agent: notices: %q", dir)
	}
	b, _ := json.Marshal(n)
	if err := sh.Upload(ctx, NoticesPath, b); err != nil {
		return err
	}
	_, err = agentCmd(ctx, sh, "notices", "seal")
	return err
}

// Status is the runtime's state, as the agent sees it.
type Status struct {
	Version string `json:"version"`
	Time    float64
	GPUs    []struct {
		Name        string `json:"name"`
		MemoryTotal int64  `json:"memory_total_mb"`
		MemoryUsed  int64  `json:"memory_used_mb"`
		Utilization int    `json:"utilization"`
	} `json:"gpus"`
	TPUDevices int     `json:"tpu_devices"`
	CPUs       int     `json:"cpus"`
	Load       float64 `json:"load"`
	Memory     struct {
		Total     int64 `json:"total"`
		Available int64 `json:"available"`
	} `json:"memory"`
	Disk struct {
		Total int64 `json:"total"`
		Free  int64 `json:"free"`
	} `json:"disk"`
	Plugins map[string]struct {
		PID     int            `json:"pid"`
		Running bool           `json:"running"`
		State   map[string]any `json:"state"`
	} `json:"plugins"`
	Watchdog struct {
		Running      bool     `json:"running"`
		Config       Watchdog `json:"config"`
		LastActivity float64  `json:"last_activity"`
	} `json:"watchdog"`
	Stopped *struct {
		Reason string  `json:"reason"`
		Time   float64 `json:"time"`
	} `json:"stopped"`
}

// VRAMMB is the memory of the runtime's GPUs together, in MiB.
func (s *Status) VRAMMB() int64 {
	var n int64
	for _, g := range s.GPUs {
		n += g.MemoryTotal
	}
	return n
}

// GetStatus asks the agent for the runtime's state.
func GetStatus(ctx context.Context, sh *colab.Shell) (*Status, error) {
	out, code, err := sh.Run(ctx, "python3 "+Path+" status")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("agent: status: %s", strings.TrimSpace(out))
	}
	var s Status
	if err := json.Unmarshal([]byte(lastLine(out)), &s); err != nil {
		return nil, fmt.Errorf("agent: status: %w", err)
	}
	return &s, nil
}

// RunPlugin uploads a plugin's script and runs it in the background.
func RunPlugin(ctx context.Context, sh *colab.Shell, id, script string) error {
	// The plugin's folder, only its owner's, before the script goes in.
	if _, err := agentCmd(ctx, sh, "prepare", id); err != nil {
		return err
	}
	path := PluginDir(id) + "/run.sh"
	// Scripts written on Windows end their lines with CRLF, which bash
	// takes for part of each command.
	script = strings.ReplaceAll(script, "\r\n", "\n")
	if err := sh.Upload(ctx, path, []byte(script)); err != nil {
		return err
	}
	out, code, err := sh.Run(ctx, fmt.Sprintf("python3 %s run %s %s", Path, id, path))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("agent: run %s: %s", id, strings.TrimSpace(out))
	}
	return nil
}

// StopPlugin stops a plugin.
func StopPlugin(ctx context.Context, sh *colab.Shell, id string) error {
	_, _, err := sh.Run(ctx, fmt.Sprintf("python3 %s stop %s", Path, id))
	return err
}

// Logs returns the end of a plugin's log.
func Logs(ctx context.Context, sh *colab.Shell, id string, lines int) (string, error) {
	out, _, err := sh.Run(ctx, fmt.Sprintf("python3 %s logs %s %d", Path, id, lines))
	return out, err
}

// Unassign has the runtime give itself back.
func Unassign(ctx context.Context, sh *colab.Shell, reason string) error {
	_, _, err := sh.Run(ctx, "python3 "+Path+" unassign "+reason)
	return err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
