package plugins

// Tasks to share: a script plugin as one JSON file, a task pack, or as a
// code to paste, and what installs one into the plugins folder.

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	// PackFormat is a task pack's "format".
	PackFormat = "colaunch-task"
	// PackVersion is the version of task packs this build writes and the
	// newest it reads.
	PackVersion = 1
	// PackExt ends the name of a task pack's file.
	PackExt = ".colaunch.json"
	// CodePrefix starts a share code: base64url of the gzipped pack.
	CodePrefix = "colaunch-task:"
	// MaxPack is the most a pack may hold, unpacked: a file, or what a
	// share code unzips to.
	MaxPack = 256 << 10

	// metaFile is where an installed task keeps who wrote it and where
	// it came from.
	metaFile = "task.json"
)

var (
	// ErrNotTask is the error of something that isn't a task pack.
	ErrNotTask = errors.New("plugins: not a Colaunch task")
	// ErrNewerTask is the error of a pack of a newer version of the format.
	ErrNewerTask = errors.New("plugins: the task was made by a newer Colaunch")
	// ErrTooBig is the error of a pack, or a download, past its limit.
	ErrTooBig = errors.New("plugins: the task is too big")
	// ErrInvalid is the error of a pack whose manifest or script is wrong.
	ErrInvalid = errors.New("plugins: the task isn't valid")
	// ErrChecksum is the error of a download that isn't what the community
	// index says.
	ErrChecksum = errors.New("plugins: the download doesn't match the index's checksum")
)

// Pack is a task in one file:
//
//	{"format": "colaunch-task", "version": 1,
//	 "manifest": {…the fields of plugin.json…},
//	 "script": "…", "readme": "…", "author": "…"}
type Pack struct {
	Format   string   `json:"format"`
	Version  int      `json:"version"`
	Manifest Manifest `json:"manifest"`
	Script   string   `json:"script"`
	Readme   string   `json:"readme,omitempty"`
	Author   string   `json:"author,omitempty"`
	// Source is where the pack was read from: a file's name, a link, or
	// "code" for a pasted share code. An installed task keeps it.
	Source string `json:"-"`
}

// Meta is what an installed task keeps beside its plugin.json.
type Meta struct {
	Author string `json:"author,omitempty"`
	// Source is where it was imported from; "" for a task written here.
	Source string `json:"source,omitempty"`
}

// Imported reports whether the task came from someone else.
func (m Meta) Imported() bool { return m.Source != "" }

// ConflictError is the error of installing a task whose ID another has.
type ConflictError struct {
	ID string
	// Builtin is true when the other can't be replaced: built in, or
	// outside the plugins folder.
	Builtin bool
	// Dir is the folder of the user's task that installing would replace.
	Dir string
}

func (e *ConflictError) Error() string {
	if e.Builtin {
		return fmt.Sprintf("plugins: %q is the ID of a built-in task: give the task another ID", e.ID)
	}
	return fmt.Sprintf("plugins: you already have a task with the ID %q: replace it or give this one another ID", e.ID)
}

// Export makes a pack of a script plugin.
func Export(p *ScriptPlugin) *Pack {
	m := cloneManifest(p.Manifest)
	m.Script = ""
	return &Pack{
		Format: PackFormat, Version: PackVersion,
		Manifest: m, Script: p.script, Readme: p.Readme, Author: p.Meta.Author,
	}
}

// NewPack makes a pack of a manifest and its script, as the editor saves.
func NewPack(m Manifest, script string) *Pack {
	return &Pack{Format: PackFormat, Version: PackVersion, Manifest: cloneManifest(m), Script: script}
}

func cloneManifest(m Manifest) Manifest {
	b, _ := json.Marshal(m)
	var out Manifest
	json.Unmarshal(b, &out)
	return out
}

func marshal(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Scripts are full of && and <: keep them readable in the file.
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// JSON is the pack as a file holds it.
func (p *Pack) JSON() ([]byte, error) {
	p.Format, p.Version = PackFormat, PackVersion
	return marshal(p, true)
}

// FileName is the name to save the pack under.
func (p *Pack) FileName() string { return p.Manifest.ID + PackExt }

// Code is the pack's share code: CodePrefix and base64url, without
// padding, of the gzipped JSON.
func (p *Pack) Code() (string, error) {
	p.Format, p.Version = PackFormat, PackVersion
	b, err := marshal(p, false)
	if err != nil {
		return "", err
	}
	if len(b) > MaxPack {
		return "", ErrTooBig
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	if err := zw.Close(); err != nil {
		return "", err
	}
	return CodePrefix + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// Parse reads a pack from a file's bytes or a share code, and checks it.
func Parse(b []byte) (*Pack, error) {
	b = bytes.TrimPrefix(bytes.TrimSpace(b), []byte("\xef\xbb\xbf"))
	if bytes.HasPrefix(b, []byte(CodePrefix)) {
		raw, err := decodeCode(b[len(CodePrefix):])
		if err != nil {
			return nil, err
		}
		b = raw
	}
	if len(b) > MaxPack {
		return nil, ErrTooBig
	}
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(b, &head); err != nil || head.Format != PackFormat {
		return nil, ErrNotTask
	}
	if head.Version > PackVersion {
		return nil, ErrNewerTask
	}
	if head.Version < 1 {
		return nil, fmt.Errorf("%w: version %d", ErrNotTask, head.Version)
	}
	var p Pack
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	normalize(&p.Manifest)
	if err := ValidateTask(p.Manifest, p.Script); err != nil {
		return nil, err
	}
	return &p, nil
}

// decodeCode unpacks a share code's body, refusing what unzips past
// MaxPack, as a gzip bomb would.
func decodeCode(body []byte) ([]byte, error) {
	clean := make([]byte, 0, len(body))
	for _, c := range body {
		switch {
		case c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '=':
		case c == '+':
			clean = append(clean, '-')
		case c == '/':
			clean = append(clean, '_')
		default:
			clean = append(clean, c)
		}
	}
	if len(clean) > base64.RawURLEncoding.EncodedLen(MaxPack) {
		return nil, ErrTooBig
	}
	zipped, err := base64.RawURLEncoding.DecodeString(string(clean))
	if err != nil {
		return nil, fmt.Errorf("%w: the share code is damaged", ErrNotTask)
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		return nil, fmt.Errorf("%w: the share code is damaged", ErrNotTask)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, MaxPack+1))
	if len(raw) > MaxPack {
		return nil, ErrTooBig
	}
	if err != nil {
		return nil, fmt.Errorf("%w: the share code is damaged", ErrNotTask)
	}
	return raw, nil
}

// normalize fills in what a manifest may leave out, as LoadScriptPlugin
// does.
func normalize(m *Manifest) {
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
		if strings.TrimSpace(p.Name) == "" {
			m.Ports[i].Name = strconv.Itoa(p.Remote)
		}
	}
}

var (
	fieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	variants        = map[string]bool{"": true, "VARIANT_CPU": true, "VARIANT_GPU": true, "VARIANT_TPU": true}
	fieldKinds      = map[FieldKind]bool{FieldText: true, FieldPassword: true, FieldNumber: true, FieldSelect: true, FieldSwitch: true}
	portKinds       = map[PortKind]bool{PortWeb: true, PortOpenAI: true, PortTCP: true}
)

// jupyterPort is Colab's own Jupyter server on the runtime.
const jupyterPort = 8080

// ValidateTask checks a task's manifest and script, as written by hand,
// shared or edited, and returns all that's wrong, wrapped in ErrInvalid.
func ValidateTask(m Manifest, script string) error {
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if !idPattern.MatchString(m.ID) {
		bad("the ID %q should be 1 to 41 lower-case letters, digits, - and _, starting with a letter or digit", m.ID)
	}
	if strings.TrimSpace(m.Name.In("en")) == "" {
		bad("the task needs a name")
	}
	for lang, s := range m.Name {
		if len([]rune(s)) > 80 {
			bad("the name in %s is longer than 80 characters", lang)
		}
	}
	if !variants[m.Variant] {
		bad("the hardware %q should be VARIANT_CPU, VARIANT_GPU or VARIANT_TPU", m.Variant)
	}
	if len(m.Fields) > 50 {
		bad("a task has at most 50 fields")
	}
	keys := map[string]bool{}
	for i, f := range m.Fields {
		name := fmt.Sprintf("field %d (%s)", i+1, f.Key)
		if !fieldKeyPattern.MatchString(f.Key) {
			bad("%s: the key should be lower-case letters, digits and _, starting with a letter, at most 40", name)
		}
		if keys[f.Key] {
			bad("%s: another field has the same key", name)
		}
		keys[f.Key] = true
		if !fieldKinds[f.Kind] {
			bad("%s: the type %q should be text, password, number, select or switch", name, f.Kind)
		}
		switch f.Kind {
		case FieldSelect:
			if len(f.Options) == 0 {
				bad("%s: a select needs options", name)
			} else if f.Default != "" && !contains(f.Options, f.Default) {
				bad("%s: the default %q isn't one of the options", name, f.Default)
			}
		case FieldSwitch:
			if f.Default != "" && f.Default != "true" && f.Default != "false" {
				bad("%s: a switch's default is true or false", name)
			}
		case FieldNumber:
			if f.Default != "" {
				if _, err := strconv.ParseFloat(f.Default, 64); err != nil {
					bad("%s: the default %q isn't a number", name, f.Default)
				}
			}
		}
	}
	if m.ModelField != "" && !keys[m.ModelField] {
		bad("model_field %q isn't the key of a field", m.ModelField)
	}
	if len(m.Ports) > 20 {
		bad("a task forwards at most 20 ports")
	}
	remotes, locals := map[int]bool{}, map[int]bool{}
	for i, p := range m.Ports {
		name := fmt.Sprintf("port %d (%s)", i+1, p.Name)
		for _, n := range []int{p.Remote, p.Local} {
			if n < 1 || n > 65535 {
				bad("%s: %d isn't a port from 1 to 65535", name, n)
			} else if n == jupyterPort {
				bad("%s: 8080 is Colab's own Jupyter server", name)
			}
		}
		if remotes[p.Remote] {
			bad("%s: another port forwards %d of the runtime", name, p.Remote)
		}
		if locals[p.Local] {
			bad("%s: another port takes %d on this computer", name, p.Local)
		}
		remotes[p.Remote], locals[p.Local] = true, true
		if !portKinds[p.Kind] {
			bad("%s: the type %q should be web, openai or tcp", name, p.Kind)
		}
		if p.Path != "" && (!strings.HasPrefix(p.Path, "/") || strings.ContainsFunc(p.Path, unicode.IsSpace)) {
			bad("%s: the path should start with / and have no spaces", name)
		}
	}
	if m.Timeout < 0 || m.Timeout > 24*60 {
		bad("the timeout should be from 0 to 1440 minutes")
	}
	if strings.ContainsRune(m.Ready, 0) || strings.ContainsRune(script, 0) {
		bad("the script and the ready check can't hold NUL bytes")
	}
	if strings.TrimSpace(script) == "" {
		bad("the script is empty")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(errs, "; "))
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SuggestID makes an ID of a task's name: lower-case letters and digits,
// the rest as "-".
func SuggestID(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		return "my-task"
	}
	return id
}

// inside reports whether path is in root, and isn't root.
func inside(root, path string) bool {
	r, err1 := filepath.Abs(root)
	p, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(r, p)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// Install writes the pack into pluginsDir as a task's folder, checked as
// LoadScriptPlugin checks any, and returns it as loaded from there; call
// LoadDir to register it. A task of the same ID is a *ConflictError: one
// built in can't be replaced, and one of the user's own only with
// overwrite.
func Install(p *Pack, pluginsDir string, overwrite bool) (*ScriptPlugin, error) {
	m := cloneManifest(p.Manifest)
	normalize(&m)
	if err := ValidateTask(m, p.Script); err != nil {
		return nil, err
	}
	dest := filepath.Join(pluginsDir, m.ID)
	if reg := Get(m.ID); reg != nil {
		sp, ok := reg.(*ScriptPlugin)
		if !ok || !inside(pluginsDir, sp.Dir) {
			return nil, &ConflictError{ID: m.ID, Builtin: true}
		}
		dest = sp.Dir
	}
	exists := false
	if fi, err := os.Lstat(dest); err == nil {
		if !fi.IsDir() || fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return nil, fmt.Errorf("plugins: %s isn't a folder Colaunch can replace", dest)
		}
		exists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if exists && !overwrite {
		return nil, &ConflictError{ID: m.ID, Dir: dest}
	}
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(pluginsDir, ".install-"+m.ID+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	os.Chmod(staging, 0o755)
	m.Script = "run.sh"
	manifest, err := marshal(m, true)
	if err != nil {
		return nil, err
	}
	script := p.Script
	if !strings.HasSuffix(script, "\n") {
		script += "\n"
	}
	files := map[string][]byte{"plugin.json": manifest, "run.sh": []byte(script)}
	if strings.TrimSpace(p.Readme) != "" {
		files["README.md"] = []byte(p.Readme)
	}
	if meta := (Meta{Author: strings.TrimSpace(p.Author), Source: p.Source}); meta != (Meta{}) {
		b, _ := marshal(meta, true)
		files[metaFile] = b
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(staging, name), body, 0o644); err != nil {
			return nil, err
		}
	}
	if sp, err := LoadScriptPlugin(staging); err != nil {
		return nil, err
	} else if sp.ID() != m.ID {
		return nil, fmt.Errorf("%w: the ID changed as it loaded", ErrInvalid)
	}
	if exists {
		old := filepath.Join(pluginsDir, ".old-"+m.ID+"-"+randomHex())
		if err := os.Rename(dest, old); err != nil {
			return nil, err
		}
		if err := os.Rename(staging, dest); err != nil {
			os.Rename(old, dest)
			return nil, err
		}
		os.RemoveAll(old)
	} else if err := os.Rename(staging, dest); err != nil {
		return nil, err
	}
	return LoadScriptPlugin(dest)
}

// Save writes a task the user edited: in place of original, a task of
// the plugins folder, or as a new one when original is nil. A new ID
// moves the task, which mustn't take another's.
func Save(p *Pack, pluginsDir string, original *ScriptPlugin) (*ScriptPlugin, error) {
	if original == nil {
		return Install(p, pluginsDir, false)
	}
	if !inside(pluginsDir, original.Dir) {
		return nil, &ConflictError{ID: original.ID(), Builtin: true}
	}
	if p.Source == "" {
		p.Source = original.Meta.Source
	}
	if p.Manifest.ID == original.ID() {
		return Install(p, pluginsDir, true)
	}
	sp, err := Install(p, pluginsDir, false)
	if err != nil {
		return nil, err
	}
	return sp, Remove(pluginsDir, original)
}

// Remove deletes a task's folder, which must be in pluginsDir.
func Remove(pluginsDir string, p *ScriptPlugin) error {
	if !inside(pluginsDir, p.Dir) {
		return fmt.Errorf("plugins: %s isn't in the plugins folder", p.Dir)
	}
	fi, err := os.Lstat(p.Dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("plugins: %s isn't a folder", p.Dir)
	}
	return os.RemoveAll(p.Dir)
}

// readOptional reads rel of dir as readInside does, nil without error
// when there's no such file.
func readOptional(dir, rel string) ([]byte, error) {
	if _, err := os.Lstat(filepath.Join(dir, rel)); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return readInside(dir, rel)
}

func randomHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
