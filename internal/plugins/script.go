package plugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
//	  "variant": "VARIANT_GPU",
//	  "hf_token": false
//	}
//
// The script gets each field as COLAUNCH_<KEY>, in upper case, and the
// Hugging Face token as HF_TOKEN only when hf_token is true.
type Manifest struct {
	ID          string  `json:"id"`
	Name        Text    `json:"name"`
	Description Text    `json:"description,omitempty"`
	Script      string  `json:"script,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
	Ports       []Port  `json:"ports,omitempty"`
	Ready       string  `json:"ready,omitempty"`
	Timeout     float64 `json:"timeout_minutes,omitempty"`
	Variant     string  `json:"variant,omitempty"`
	Model       string  `json:"model,omitempty"`
	// ModelField names the field whose value is the model name the
	// in-app chat sends, in place of Model, as a model to pull.
	ModelField string `json:"model_field,omitempty"`
	// HFToken asks for the Hugging Face token of the settings, as
	// HF_TOKEN. Plugins don't get it otherwise.
	HFToken bool `json:"hf_token,omitempty"`
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
	// Meta is who wrote the task and where it was imported from, from
	// the folder's task.json; zero for a task written here.
	Meta Meta
	// Readme is the folder's README.md, "" without one.
	Readme string
	script string
}

// Script is the plugin's bash script, as its file holds it.
func (p *ScriptPlugin) Script() string { return p.script }

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]{0,40}$`)

// maxPluginFile is the most plugin.json or a script may hold.
const maxPluginFile = 4 << 20

// readInside reads rel, a regular file of dir. rel must stay inside dir:
// not absolute, no "..", and through no symbolic link, junction or other
// reparse point, which could lead out of it.
func readInside(dir, rel string) ([]byte, error) {
	bad := func(why string) error {
		return fmt.Errorf("%s: %q %s", dir, rel, why)
	}
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return nil, bad("should be a path inside the plugin's folder")
	}
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return nil, bad("should be a path inside the plugin's folder")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if !filepath.IsLocal(clean) {
		return nil, bad("should be a path inside the plugin's folder")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	parts := strings.Split(clean, string(filepath.Separator))
	for i := range parts {
		fi, err := root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return nil, err
		}
		switch {
		case fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0:
			return nil, bad("is a link, which may lead out of the plugin's folder")
		case i < len(parts)-1 && !fi.IsDir():
			return nil, bad("isn't a file")
		case i == len(parts)-1 && !fi.Mode().IsRegular():
			return nil, bad("isn't a regular file")
		}
	}
	f, err := root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, bad("isn't a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPluginFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPluginFile {
		return nil, bad("is too big")
	}
	return b, nil
}

// LoadScriptPlugin reads the plugin in dir.
func LoadScriptPlugin(dir string) (*ScriptPlugin, error) {
	b, err := readInside(dir, "plugin.json")
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
	if !idPattern.MatchString(m.ID) {
		return nil, fmt.Errorf("%s: the id %q should be lower case letters, digits, - and _", dir, m.ID)
	}
	if m.Script == "" {
		m.Script = "run.sh"
	}
	script, err := readInside(dir, m.Script)
	if err != nil {
		return nil, err
	}
	var meta Meta
	if b, err := readOptional(dir, metaFile); err != nil {
		return nil, err
	} else if b != nil {
		if err := json.Unmarshal(b, &meta); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", dir, metaFile, err)
		}
	}
	readme, err := readOptional(dir, "README.md")
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
	return &ScriptPlugin{Dir: dir, Manifest: m, Meta: meta, Readme: string(readme), script: string(script)}, nil
}

// LoadDir registers the plugins in the folders of dir, replacing those
// loaded from it before, and returns the errors of those it couldn't load,
// as those whose ID another plugin, built in or not, has already.
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
		// Folders whose name starts with a dot are tasks being installed.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p, err := LoadScriptPlugin(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := Register(p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Dir, err))
		}
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
	env.Phase("start")
	ready := p.Manifest.Ready
	if ready == "" && len(p.Manifest.Ports) > 0 {
		ready = fmt.Sprintf("(exec 3<>/dev/tcp/127.0.0.1/%d) 2>/dev/null", p.Manifest.Ports[0].Remote)
	}
	timeout := time.Duration(p.Manifest.Timeout * float64(time.Minute))
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	if err := deployScript(ctx, env, p.ID(), p.scriptFor(env), ready, timeout); err != nil {
		return nil, err
	}
	env.Phase("ready")
	ports := append([]Port(nil), p.Manifest.Ports...)
	model := p.Manifest.Model
	if k := p.Manifest.ModelField; k != "" && strings.TrimSpace(env.Values[k]) != "" {
		model = strings.TrimSpace(env.Values[k])
	}
	return &Result{Ports: ports, Model: model}, nil
}

// scriptFor is the script that runs on the runtime: the fields as
// variables, the Hugging Face token only for a plugin that asks for it,
// then the plugin's script.
func (p *ScriptPlugin) scriptFor(env *Env) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	for _, k := range sortedKeys(env.Values) {
		name := "COLAUNCH_" + strings.ToUpper(regexp.MustCompile(`\W`).ReplaceAllString(k, "_"))
		fmt.Fprintf(&b, "export %s=%s\n", name, ShellQuote(env.Values[k]))
	}
	if p.Manifest.HFToken && env.HFToken != "" {
		fmt.Fprintf(&b, "export HF_TOKEN=%s\n", ShellQuote(env.HFToken))
	}
	b.WriteString("cd \"$COLAUNCH_PLUGIN_DIR\"\n")
	b.WriteString(p.script)
	return b.String()
}

func (p *ScriptPlugin) Stop(ctx context.Context, sh *colab.Shell) error {
	return stopPlugin(ctx, sh, p.ID())
}

func deployScript(ctx context.Context, env *Env, id, script, check string, timeout time.Duration) error {
	if check == "" || env.ReadWaitStatus == nil {
		if err := agent.RunPlugin(ctx, env.Shell, id, script); err != nil || check == "" {
			return err
		}
		return waitReady(ctx, env, id, timeout, check, "")
	}
	var launchID [16]byte
	rand.Read(launchID[:])
	nonce := hex.EncodeToString(launchID[:])
	if err := agent.RunMonitoredPlugin(ctx, env.Shell, id, script, check, nonce, timeout); err != nil {
		return err
	}
	return waitReady(ctx, env, id, timeout, check, nonce)
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
