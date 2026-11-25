package plugins

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPack() *Pack {
	return &Pack{
		Format: PackFormat, Version: PackVersion, Author: "Ada",
		Manifest: Manifest{
			ID:   "comfy",
			Name: Text{"en": "Comfy", "zh-Hans": "康菲"},
			Fields: []Field{
				{Key: "model", Label: Text{"en": "Model"}, Kind: FieldText, Required: true},
				{Key: "mode", Kind: FieldSelect, Options: []string{"a", "b"}, Default: "b"},
			},
			Ports:      []Port{{Name: "UI", Remote: 8188, Local: 18188, Kind: PortWeb, Path: "/"}},
			Variant:    "VARIANT_GPU",
			ModelField: "model",
		},
		Script: "echo <&> \"$COLAUNCH_MODEL\"\nexec python3 -m http.server 8188 --bind 127.0.0.1\n",
		Readme: "# Comfy\n",
	}
}

// A pack goes through its file and its share code unchanged.
func TestPackRoundTrips(t *testing.T) {
	p := testPack()
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("<&>")) {
		t.Errorf("the file escapes the script:\n%s", b)
	}
	code, err := p.Code()
	if err != nil || !strings.HasPrefix(code, CodePrefix) || strings.ContainsAny(code, "+/=") {
		t.Fatalf("code %q, %v", code, err)
	}
	// Pasted codes may be wrapped and padded.
	body := code[len(CodePrefix):]
	wrapped := CodePrefix + body[:10] + "\n  " + body[10:] + "=="
	for name, in := range map[string][]byte{"file": b, "code": []byte(code), "wrapped": []byte(wrapped), "bom": append([]byte("\xef\xbb\xbf"), b...)} {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.Script != p.Script || got.Author != "Ada" || got.Readme != p.Readme || got.Manifest.Name.In("zh-Hans") != "康菲" ||
			got.Manifest.Ports[0].Local != 18188 || got.Manifest.Fields[1].Default != "b" || got.Manifest.ModelField != "model" {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func gzipCode(raw []byte) string {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	return CodePrefix + base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

// What isn't a pack, is too big or unzips too big, is refused.
func TestParseRefuses(t *testing.T) {
	big := testPack()
	big.Script = strings.Repeat("echo hi\n", MaxPack/8+1)
	bigJSON, _ := big.JSON()
	// A gzip bomb: a small code that unzips to megabytes.
	bomb := gzipCode(append([]byte(`{"format":"colaunch-task","version":1,"script":"`), make([]byte, 8<<20)...))
	newer := strings.Replace(string(mustJSON(t, testPack())), `"version": 1`, `"version": 2`, 1)
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrNotTask},
		{"other json", `{"id": "x"}`, ErrNotTask},
		{"plugin.json", `{"format": "colaunch-plugin", "version": 1}`, ErrNotTask},
		{"version 0", `{"format": "colaunch-task", "version": 0}`, ErrNotTask},
		{"newer", newer, ErrNewerTask},
		{"damaged code", CodePrefix + "!!!!", ErrNotTask},
		{"not gzip", CodePrefix + base64.RawURLEncoding.EncodeToString([]byte("hello")), ErrNotTask},
		{"big file", string(bigJSON), ErrTooBig},
		{"bomb", bomb, ErrTooBig},
		{"long code", CodePrefix + strings.Repeat("A", MaxPack*2), ErrTooBig},
		{"no script", `{"format": "colaunch-task", "version": 1, "manifest": {"id": "x", "name": "X"}, "script": "  \n"}`, ErrInvalid},
	}
	for _, c := range cases {
		if _, err := Parse([]byte(c.in)); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if len(bomb) > 64<<10 {
		t.Fatalf("the bomb is %d bytes: it should be small", len(bomb))
	}
}

func mustJSON(t *testing.T, p *Pack) []byte {
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Manifests are checked as they are shared and edited.
func TestValidateTask(t *testing.T) {
	ok := testPack()
	if err := ValidateTask(ok.Manifest, ok.Script); err != nil {
		t.Fatalf("a good task: %v", err)
	}
	cases := map[string]func(m *Manifest){
		"id upper":        func(m *Manifest) { m.ID = "Comfy" },
		"id dots":         func(m *Manifest) { m.ID = "../x" },
		"id empty":        func(m *Manifest) { m.ID = "" },
		"no name":         func(m *Manifest) { m.Name = Text{} },
		"variant":         func(m *Manifest) { m.Variant = "VARIANT_QUANTUM" },
		"field key":       func(m *Manifest) { m.Fields[0].Key = "Model Name" },
		"field key digit": func(m *Manifest) { m.Fields[0].Key = "1model" },
		"field key dup":   func(m *Manifest) { m.Fields[1].Key = "model" },
		"field kind":      func(m *Manifest) { m.Fields[0].Kind = "file" },
		"select options":  func(m *Manifest) { m.Fields[1].Options = nil },
		"select default":  func(m *Manifest) { m.Fields[1].Default = "c" },
		"switch default":  func(m *Manifest) { m.Fields[1] = Field{Key: "on", Kind: FieldSwitch, Default: "yes"} },
		"number default":  func(m *Manifest) { m.Fields[1] = Field{Key: "n", Kind: FieldNumber, Default: "ten"} },
		"model field":     func(m *Manifest) { m.ModelField = "nothing" },
		"port 0":          func(m *Manifest) { m.Ports[0].Remote = 0 },
		"port 65536":      func(m *Manifest) { m.Ports[0].Remote = 65536 },
		"port negative":   func(m *Manifest) { m.Ports[0].Local = -1 },
		"port 8080":       func(m *Manifest) { m.Ports[0].Remote = 8080 },
		"local 8080":      func(m *Manifest) { m.Ports[0].Local = 8080 },
		"port twice": func(m *Manifest) {
			m.Ports = append(m.Ports, Port{Name: "b", Remote: 8188, Local: 9000, Kind: PortTCP})
		},
		"local twice": func(m *Manifest) {
			m.Ports = append(m.Ports, Port{Name: "b", Remote: 9000, Local: 18188, Kind: PortTCP})
		},
		"port kind":        func(m *Manifest) { m.Ports[0].Kind = "udp" },
		"port path":        func(m *Manifest) { m.Ports[0].Path = "lab" },
		"timeout":          func(m *Manifest) { m.Timeout = 99999 },
		"timeout negative": func(m *Manifest) { m.Timeout = -1 },
	}
	for name, change := range cases {
		p := testPack()
		change(&p.Manifest)
		if err := ValidateTask(p.Manifest, p.Script); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := ValidateTask(ok.Manifest, " \n\t"); !errors.Is(err, ErrInvalid) {
		t.Error("an empty script passed")
	}
	if err := ValidateTask(ok.Manifest, "echo \x00"); !errors.Is(err, ErrInvalid) {
		t.Error("a NUL passed")
	}
}

func TestSuggestID(t *testing.T) {
	for in, want := range map[string]string{
		"ComfyUI":                   "comfyui",
		"  Stable Diffusion WebUI ": "stable-diffusion-webui",
		"Ollama (Qwen 3)":           "ollama-qwen-3",
		"我的任务":                      "my-task",
		"":                          "my-task",
		"--x--":                     "x",
		strings.Repeat("a", 60):     strings.Repeat("a", 40),
	} {
		got := SuggestID(in)
		if got != want || !idPattern.MatchString(got) {
			t.Errorf("SuggestID(%q) = %q, want %q", in, got, want)
		}
	}
}

func installed(t *testing.T, root string) []string {
	t.Helper()
	entries, _ := os.ReadDir(root)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Installing writes a folder LoadDir loads, with the script as run.sh,
// whatever the pack's manifest names, and keeps where the task came from.
func TestInstall(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { LoadDir(t.TempDir()) })
	p := testPack()
	p.Manifest.Script = "../../etc/passwd"
	p.Source = "https://example.com/comfy.colaunch.json"
	sp, err := Install(p, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Dir != filepath.Join(root, "comfy") || sp.Manifest.Script != "run.sh" || sp.Script() != p.Script {
		t.Errorf("installed %+v", sp)
	}
	if sp.Meta.Author != "Ada" || sp.Meta.Source != p.Source || !sp.Meta.Imported() || sp.Readme != p.Readme {
		t.Errorf("meta %+v, readme %q", sp.Meta, sp.Readme)
	}
	if errs := LoadDir(root); len(errs) > 0 {
		t.Fatal(errs)
	}
	if got, ok := Get("comfy").(*ScriptPlugin); !ok || got.Manifest.Ports[0].Local != 18188 {
		t.Fatalf("comfy is %+v", Get("comfy"))
	}
	// Exporting it again gives the same pack.
	again := Export(Get("comfy").(*ScriptPlugin))
	if again.Script != p.Script || again.Author != "Ada" || again.Manifest.Script != "" || again.Manifest.ModelField != "model" {
		t.Errorf("exported %+v", again)
	}

	// The same ID again asks; replacing works and leaves nothing behind.
	var conflict *ConflictError
	if _, err := Install(p, root, false); !errors.As(err, &conflict) || conflict.Builtin || conflict.Dir != sp.Dir {
		t.Fatalf("installing twice: %v", err)
	}
	p.Script = "echo new\n"
	if _, err := Install(p, root, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "comfy", "run.sh")); string(b) != "echo new\n" {
		t.Errorf("run.sh is %q after replacing", b)
	}
	if names := installed(t, root); len(names) != 1 {
		t.Errorf("the plugins folder has %v", names)
	}

	// A built-in task's ID can't be taken, even replacing.
	llama := testPack()
	llama.Manifest.ID = "llama"
	if _, err := Install(llama, root, true); !errors.As(err, &conflict) || !conflict.Builtin {
		t.Errorf("installing llama: %v", err)
	}
	// A folder of that name, not loaded, is the user's to replace.
	os.MkdirAll(filepath.Join(root, "other"), 0o755)
	other := testPack()
	other.Manifest.ID = "other"
	if _, err := Install(other, root, false); !errors.As(err, &conflict) {
		t.Errorf("installing over a folder: %v", err)
	}
	// An invalid pack writes nothing.
	bad := testPack()
	bad.Manifest.ID, bad.Manifest.Ports[0].Remote = "bad", 8080
	if _, err := Install(bad, root, true); !errors.Is(err, ErrInvalid) {
		t.Errorf("installing a bad pack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bad")); err == nil {
		t.Error("a bad pack was written")
	}
}

// Saving an edited task with a new ID moves it; another's ID is refused.
func TestSaveMoves(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { LoadDir(t.TempDir()) })
	p := testPack()
	p.Source = "code"
	if _, err := Install(p, root, false); err != nil {
		t.Fatal(err)
	}
	q := testPack()
	q.Manifest.ID = "taken"
	if _, err := Install(q, root, false); err != nil {
		t.Fatal(err)
	}
	LoadDir(root)
	orig := Get("comfy").(*ScriptPlugin)

	edited := Export(orig)
	edited.Manifest.ID = "taken"
	var conflict *ConflictError
	if _, err := Save(edited, root, orig); !errors.As(err, &conflict) {
		t.Fatalf("saving over another task: %v", err)
	}
	edited = Export(orig)
	edited.Manifest.ID = "comfy2"
	sp, err := Save(edited, root, orig)
	if err != nil {
		t.Fatal(err)
	}
	if sp.ID() != "comfy2" || sp.Meta.Source != "code" {
		t.Errorf("saved %+v", sp)
	}
	if _, err := os.Stat(orig.Dir); err == nil {
		t.Error("the old folder stayed")
	}
	if err := Remove(root, &ScriptPlugin{Dir: t.TempDir()}); err == nil {
		t.Error("removed a folder outside the plugins folder")
	}
	if err := Remove(root, &ScriptPlugin{Dir: root}); err == nil {
		t.Error("removed the plugins folder")
	}
}

// LoadDir leaves out folders being installed.
func TestLoadDirSkipsStaging(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { LoadDir(t.TempDir()) })
	writePlugin(t, filepath.Join(root, ".install-x-1"), `{"id": "x"}`, map[string]string{"run.sh": "true\n"})
	if errs := LoadDir(root); len(errs) > 0 || Get("x") != nil {
		t.Errorf("errors %v, x %v", errs, Get("x"))
	}
}

func TestRawURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/VOfficial404/colaunch/blob/main/tasks/ollama.colaunch.json": "https://raw.githubusercontent.com/VOfficial404/colaunch/main/tasks/ollama.colaunch.json",
		"https://github.com/a/b/raw/v1.0/x/y.colaunch.json":                             "https://raw.githubusercontent.com/a/b/v1.0/x/y.colaunch.json",
		"https://example.com/t.colaunch.json#frag":                                      "https://example.com/t.colaunch.json",
		"  https://github.com/a/b  ":                                                    "https://github.com/a/b",
	} {
		if got, err := RawURL(in); err != nil || got != want {
			t.Errorf("RawURL(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"http://example.com/t.json", "file:///etc/passwd", "ftp://x/y", "example.com/t.json", "https://user:pw@example.com/t"} {
		if _, err := RawURL(in); err == nil {
			t.Errorf("RawURL(%q) worked", in)
		}
	}
}

// Downloads are https, small and quick; redirects stay https.
func TestFetch(t *testing.T) {
	pack := mustJSON(t, testPack())
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.colaunch.json":
			w.Write(pack)
		case "/big":
			w.Write(make([]byte, MaxPack+10))
		case "/plain":
			http.Redirect(w, r, "http://example.com/x", http.StatusFound)
		case "/slow":
			time.Sleep(2 * time.Second)
			w.Write(pack)
		case "/index.json":
			fmt.Fprintf(w, `{"version": 1, "tasks": [
				{"id": "comfy", "name": "Comfy", "path": "ok.colaunch.json", "sha256": "%s"},
				{"id": "wrong", "name": "Wrong", "path": "ok.colaunch.json", "sha256": "%s"},
				{"id": "escape", "path": "../x.colaunch.json", "sha256": "%s"},
				{"id": "nosum", "path": "ok.colaunch.json"}]}`, sha(pack), strings.Repeat("0", 64), sha(pack))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	saved := HTTPClient
	defer func() { HTTPClient = saved }()
	client := srv.Client()
	client.Timeout, client.CheckRedirect = time.Second, saved.CheckRedirect
	HTTPClient = client
	ctx := context.Background()

	p, err := FetchPack(ctx, srv.URL+"/ok.colaunch.json")
	if err != nil || p.Manifest.ID != "comfy" || p.Source != srv.URL+"/ok.colaunch.json" {
		t.Fatalf("fetched %+v, %v", p, err)
	}
	if _, err := FetchPack(ctx, srv.URL+"/big"); !errors.Is(err, ErrTooBig) {
		t.Errorf("big: %v", err)
	}
	if _, err := FetchPack(ctx, srv.URL+"/plain"); !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("redirect to http: %v", err)
	}
	if _, err := FetchPack(ctx, srv.URL+"/slow"); err == nil {
		t.Error("a slow server didn't time out")
	}
	if _, err := FetchPack(ctx, srv.URL+"/missing"); err == nil {
		t.Error("a 404 worked")
	}
	if _, err := FetchPack(ctx, strings.Replace(srv.URL, "https", "http", 1)+"/ok.colaunch.json"); !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("http: %v", err)
	}

	entries, err := FetchIndex(ctx, srv.URL+"/index.json")
	if err != nil || len(entries) != 2 {
		t.Fatalf("index %+v, %v", entries, err)
	}
	p, err = FetchEntry(ctx, srv.URL+"/index.json", entries[0])
	if err != nil || p.Manifest.ID != "comfy" {
		t.Errorf("entry: %v", err)
	}
	if _, err := FetchEntry(ctx, srv.URL+"/index.json", entries[1]); !errors.Is(err, ErrChecksum) {
		t.Errorf("a wrong checksum: %v", err)
	}
	// Right checksum, wrong ID.
	e := entries[0]
	e.ID = "other"
	if _, err := FetchEntry(ctx, srv.URL+"/index.json", e); !errors.Is(err, ErrInvalid) {
		t.Errorf("a wrong id: %v", err)
	}
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestParseIndexList(t *testing.T) {
	entries, err := ParseIndex([]byte(`[{"id": "a", "path": "a.colaunch.json", "sha256": "` + strings.Repeat("AB", 32) + `"}]`))
	if err != nil || len(entries) != 1 || entries[0].Name.In("en") != "a" || entries[0].SHA256 != strings.Repeat("ab", 32) {
		t.Errorf("%+v, %v", entries, err)
	}
	if _, err := ParseIndex([]byte("nope")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestParseDeepLink(t *testing.T) {
	code, _ := testPack().Code()
	body := strings.TrimPrefix(code, CodePrefix)
	for in, want := range map[string][2]string{
		"colaunch://import?code=" + code:                                 {code, ""},
		"colaunch://import?code=" + body:                                 {code, ""},
		"COLAUNCH://Import?url=https%3A%2F%2Fexample.com%2Fa.json":       {"", "https://example.com/a.json"},
		"colaunch:///import?url=https://github.com/a/b/blob/main/x.json": {"", "https://github.com/a/b/blob/main/x.json"},
	} {
		c, u, err := ParseDeepLink(in)
		if err != nil || c != want[0] || u != want[1] {
			t.Errorf("%q: %q %q %v", in, c, u, err)
		}
	}
	for _, in := range []string{
		"colaunch://import?url=http://example.com/a.json",
		"colaunch://import",
		"colaunch://delete?code=x",
		"https://example.com/?code=x",
		"colaunch://import?url=file:///etc/passwd",
	} {
		if _, _, err := ParseDeepLink(in); err == nil {
			t.Errorf("%q worked", in)
		}
	}
	c, _, _ := ParseDeepLink("colaunch://import?code=" + code)
	if p, err := Parse([]byte(c)); err != nil || p.Manifest.ID != "comfy" {
		t.Errorf("the link's code: %v", err)
	}
}
