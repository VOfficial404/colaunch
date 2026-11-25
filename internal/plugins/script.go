package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
)

// Manifest is a plugin.json: a plugin users write without Go, as a bash
// script that runs on the runtime.
//
//	{
//	  "id": "comfyui",
//	  "name": {"en": "ComfyUI", "zh-Hans": "ComfyUI"},
//	  "description": "Node-based Stable Diffusion",
//	  "script": "run.sh",
//	  "fields": [{"key": "model", "label": "Checkpoint URL", "kind": "text"}],
//	  "ports": [{"name": "ComfyUI", "remote": 8188, "local": 8188, "kind": "web"}],
//	  "ready": "curl -sf http://127.0.0.1:8188/ >/dev/null",
//	  "timeout_minutes": 30,
//	  "variant": "VARIANT_GPU"
//	}
//
// The script gets each field as COLAUNCH_<KEY>, in upper case.
type Manifest struct {
	ID          string  `json:"id"`
	Name        Text    `json:"name"`
	Description Text    `json:"description"`
	Script      string  `json:"script"`
	Fields      []Field `json:"fields"`
	Ports       []Port  `json:"ports"`
	Ready       string  `json:"ready"`
	Timeout     float64 `json:"timeout_minutes"`
	Variant     string  `json:"variant"`
	Model       string  `json:"model"`
}

// UnmarshalJSON lets a Text be a plain string.
func (t *Text) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = Text{"en": s}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*t = m
	return nil
}

// ScriptPlugin is a plugin of a folder with a plugin.json.
type ScriptPlugin struct {
	Dir      string
	Manifest Manifest
	script   string
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]{0,40}$`)

// LoadScriptPlugin reads the plugin in dir.
func LoadScriptPlugin(dir string) (*ScriptPlugin, error) {
	b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if m.ID == "" {
		m.ID = filepath.Base(dir)
	}
	if !idPattern.MatchString(m.ID) || m.ID == "llama" {
		return nil, fmt.Errorf("%s: the id %q should be lower case letters, digits, - and _", dir, m.ID)
	}
	if m.Script == "" {
		m.Script = "run.sh"
	}
	script, err := os.ReadFile(filepath.Join(dir, m.Script))
	if err != nil {
		return nil, err
	}
	if len(m.Name) == 0 {
		m.Name = Text{"en": m.ID}
	}
	for i, f := range m.Fields {
		if f.Kind == "" {
			m.Fields[i].Kind = FieldText
		}
		if len(f.Label) == 0 {
			m.Fields[i].Label = Text{"en": f.Key}
		}
	}
	for i, p := range m.Ports {
		if p.Local == 0 {
			m.Ports[i].Local = p.Remote
		}
		if p.Kind == "" {
			m.Ports[i].Kind = PortTCP
		}
	}
	return &ScriptPlugin{Dir: dir, Manifest: m, script: string(script)}, nil
}

// LoadDir registers the plugins in the folders of dir, replacing those
// loaded before, and returns the errors of those it couldn't load.
func LoadDir(dir string) []error {
	for _, p := range All() {
		if _, ok := p.(*ScriptPlugin); ok {
			Unregister(p.ID())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return []error{err}
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := LoadScriptPlugin(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		Register(p)
	}
	return errs
}

func (p *ScriptPlugin) ID() string        { return p.Manifest.ID }
func (p *ScriptPlugin) Name() Text        { return p.Manifest.Name }
func (p *ScriptPlugin) Description() Text { return p.Manifest.Description }
func (p *ScriptPlugin) Fields() []Field   { return p.Manifest.Fields }

func (p *ScriptPlugin) Validate(ctx context.Context, values map[string]string, _ string) (*Hardware, error) {
	if err := checkRequired(p, values, "en"); err != nil {
		return nil, err
	}
	if p.Manifest.Variant == "" {
		return nil, nil
	}
	return &Hardware{Variant: colab.Variant(p.Manifest.Variant)}, nil
}

func (p *ScriptPlugin) Deploy(ctx context.Context, env *Env) (*Result, error) {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	for _, k := range sortedKeys(env.Values) {
		name := "COLAUNCH_" + strings.ToUpper(regexp.MustCompile(`\W`).ReplaceAllString(k, "_"))
		fmt.Fprintf(&b, "export %s=%s\n", name, ShellQuote(env.Values[k]))
	}
	if env.HFToken != "" {
		fmt.Fprintf(&b, "export HF_TOKEN=%s\n", ShellQuote(env.HFToken))
	}
	b.WriteString("cd \"$COLAUNCH_PLUGIN_DIR\"\n")
	b.WriteString(p.script)
	env.Phase("start")
	if err := runScript(ctx, env, p.ID(), b.String()); err != nil {
		return nil, err
	}
	ready := p.Manifest.Ready
	if ready == "" && len(p.Manifest.Ports) > 0 {
		ready = fmt.Sprintf("(exec 3<>/dev/tcp/127.0.0.1/%d) 2>/dev/null", p.Manifest.Ports[0].Remote)
	}
	if ready != "" {
		timeout := time.Duration(p.Manifest.Timeout * float64(time.Minute))
		if timeout <= 0 {
			timeout = 30 * time.Minute
		}
		if err := waitReady(ctx, env, p.ID(), timeout, ready); err != nil {
			return nil, err
		}
	}
	env.Phase("ready")
	ports := append([]Port(nil), p.Manifest.Ports...)
	return &Result{Ports: ports, Model: p.Manifest.Model}, nil
}

func (p *ScriptPlugin) Stop(ctx context.Context, sh *colab.Shell) error {
	return stopPlugin(ctx, sh, p.ID())
}

func runScript(ctx context.Context, env *Env, id, script string) error {
	return agent.RunPlugin(ctx, env.Shell, id, script)
}

func stopPlugin(ctx context.Context, sh *colab.Shell, id string) error {
	return agent.StopPlugin(ctx, sh, id)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Example is a plugin users can start from, written into the plugins
// folder when it's empty.
var Example = map[string]string{
	"plugin.json": `{
  "id": "jupyter-lab",
  "name": {"en": "JupyterLab", "zh-Hans": "JupyterLab", "zh-Hant": "JupyterLab"},
  "description": {
    "en": "An example plugin: JupyterLab on the runtime, at 127.0.0.1:8888.",
    "zh-Hans": "示例插件：在运行时上启动 JupyterLab，映射到 127.0.0.1:8888。",
    "zh-Hant": "範例外掛：在執行階段上啟動 JupyterLab，對應到 127.0.0.1:8888。"
  },
  "script": "run.sh",
  "fields": [
    {"key": "token", "label": {"en": "Token", "zh-Hans": "访问令牌", "zh-Hant": "存取權杖"}, "kind": "password", "default": "colaunch"}
  ],
  "ports": [{"name": "JupyterLab", "remote": 8888, "local": 8888, "kind": "web", "path": "/lab"}],
  "ready": "curl -sf -o /dev/null http://127.0.0.1:8888/api",
  "timeout_minutes": 10
}
`,
	"run.sh": `# Runs on the runtime. Fields arrive as COLAUNCH_<KEY>.
pip install -q jupyterlab
exec jupyter lab --no-browser --ip=127.0.0.1 --port=8888 \
  --ServerApp.token="$COLAUNCH_TOKEN" --ServerApp.root_dir=/content
`,
}
