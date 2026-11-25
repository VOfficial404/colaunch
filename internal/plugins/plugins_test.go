package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writePlugin(t *testing.T, dir, manifest string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A plugin's script is a regular file of its folder: no absolute path, no
// "..", nothing reached through a link.
func TestScriptStaysInsideItsFolder(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "secret.sh")
	os.WriteFile(outside, []byte("cat ~/.ssh/id_rsa\n"), 0o644)

	ok := filepath.Join(root, "ok")
	writePlugin(t, ok, `{"id": "ok", "script": "bin/run.sh"}`, map[string]string{"bin/run.sh": "echo hi\n"})
	if p, err := LoadScriptPlugin(ok); err != nil || p.script != "echo hi\n" {
		t.Fatalf("a script in a subfolder: %v", err)
	}

	for name, script := range map[string]string{
		"dotdot":   "../secret.sh",
		"dotdot2":  "sub/../../secret.sh",
		"absolute": filepath.ToSlash(outside),
		"slash":    "/etc/passwd",
		"dir":      "sub",
	} {
		dir := filepath.Join(root, name)
		writePlugin(t, dir, `{"id": "`+name+`", "script": "`+script+`"}`, map[string]string{"sub/x": ""})
		if _, err := LoadScriptPlugin(dir); err == nil {
			t.Errorf("%s: the script %q loaded", name, script)
		}
	}

	// A link to a file outside, and a folder that's a link (a junction
	// on Windows) to outside.
	link := filepath.Join(root, "link")
	writePlugin(t, link, `{"id": "link", "script": "run.sh"}`, nil)
	linked := os.Symlink(outside, filepath.Join(link, "run.sh")) == nil
	if linked {
		if _, err := LoadScriptPlugin(link); err == nil {
			t.Error("a script that's a symbolic link loaded")
		}
	}
	via := filepath.Join(root, "via")
	writePlugin(t, via, `{"id": "via", "script": "out/secret.sh"}`, nil)
	made := os.Symlink(root, filepath.Join(via, "out")) == nil
	if !made && runtime.GOOS == "windows" {
		made = exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(via, "out"), root).Run() == nil
	}
	if made {
		if _, err := LoadScriptPlugin(via); err == nil {
			t.Error("a script through a linked folder loaded")
		}
	}
	t.Logf("symlink %v, linked folder %v", linked, made)
	if !linked && !made {
		t.Log("can't make links here: those cases weren't tried")
	}
}

// A plugin can't take the ID of one already registered, built in or not.
func TestDuplicateIDsAreRefused(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, filepath.Join(root, "a"), `{"id": "same"}`, map[string]string{"run.sh": "true\n"})
	writePlugin(t, filepath.Join(root, "b"), `{"id": "same"}`, map[string]string{"run.sh": "false\n"})
	writePlugin(t, filepath.Join(root, "c"), `{"id": "llama"}`, map[string]string{"run.sh": "true\n"})
	t.Cleanup(func() { LoadDir(t.TempDir()) })
	errs := LoadDir(root)
	if len(errs) != 2 {
		t.Fatalf("errors %v, want two: the second \"same\" and \"llama\"", errs)
	}
	if _, ok := Get("llama").(Llama); !ok {
		t.Error("the built-in llama plugin was replaced")
	}
	p, ok := Get("same").(*ScriptPlugin)
	if !ok || p.script != "true\n" {
		t.Errorf("same is %+v, want the first one", Get("same"))
	}
	if err := Register(Llama{}); err == nil {
		t.Error("registering llama again worked")
	}
	// Loading again replaces the folder's plugins, without errors of
	// their own IDs.
	if errs := LoadDir(root); len(errs) != 2 {
		t.Errorf("loading again: %v", errs)
	}
}

// Plugins get the Hugging Face token only when their plugin.json asks.
func TestHFTokenOnlyWhenAsked(t *testing.T) {
	env := &Env{Values: map[string]string{"model": "x"}, HFToken: "hf_secret"}
	p := &ScriptPlugin{Manifest: Manifest{ID: "p"}, script: "true\n"}
	if s := p.scriptFor(env); strings.Contains(s, "hf_secret") || strings.Contains(s, "HF_TOKEN") {
		t.Errorf("a plugin that didn't ask got the token:\n%s", s)
	}
	p.Manifest.HFToken = true
	if s := p.scriptFor(env); !strings.Contains(s, "export HF_TOKEN='hf_secret'") {
		t.Errorf("a plugin that asked didn't get the token:\n%s", s)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(`{"id": "p", "hf_token": true}`), &m); err != nil || !m.HFToken {
		t.Errorf("hf_token: %v %v", m.HFToken, err)
	}
}

func TestReleaseAsset(t *testing.T) {
	api, name, ok := releaseAsset(DefaultCUDAURL)
	if !ok || name != "llama-cuda-linux-x64.tar.gz" ||
		api != "https://api.github.com/repos/VOfficial404/colaunch/releases/tags/llama-cuda" {
		t.Errorf("default: %q %q %v", api, name, ok)
	}
	for _, u := range []string{
		"https://example.com/llama.tar.gz",
		"http://github.com/a/b/releases/download/t/x.tar.gz",
		"https://github.com/a/b/archive/t/x.tar.gz",
		"https://github.com/a/../releases/download/t/x.tar.gz",
	} {
		if _, _, ok := releaseAsset(u); ok {
			t.Errorf("%s read as a release's asset", u)
		}
	}
	if _, _, _, custom := cudaSource(map[string]string{}); custom {
		t.Error("no address in the settings is custom")
	}
	if u, _, _, custom := cudaSource(map[string]string{"llama_cuda_url": " https://example.com/l.tar.gz "}); !custom || u != "https://example.com/l.tar.gz" {
		t.Errorf("an address of the settings: %q custom %v", u, custom)
	}
}

// llama.sh unpacks llama.cpp with Python, keeping regular files inside
// the folder only.
func TestSafeExtract(t *testing.T) {
	py := os.Getenv("COLAUNCH_TEST_PYTHON")
	if py == "" {
		var err error
		if py, err = exec.LookPath("python3"); err != nil || runtime.GOOS == "windows" {
			t.Skip("no Python: set COLAUNCH_TEST_PYTHON")
		}
	}
	start := strings.Index(llamaScript, "<<'PY'\n")
	end := strings.Index(llamaScript[start:], "\nPY\n")
	if start < 0 || end < 0 {
		t.Fatal("no safe_extract in llama.sh")
	}
	code := llamaScript[start+len("<<'PY'\n") : start+end+1]
	helper := filepath.Join(t.TempDir(), "extract.py")
	os.WriteFile(helper, []byte(code), 0o644)

	archive := func(t *testing.T, entries ...*tar.Header) string {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, h := range entries {
			body := []byte("data of " + h.Name)
			if h.Typeflag == tar.TypeReg {
				h.Size = int64(len(body))
			}
			tw.WriteHeader(h)
			if h.Typeflag == tar.TypeReg {
				tw.Write(body)
			}
		}
		tw.Close()
		gz.Close()
		p := filepath.Join(t.TempDir(), "a.tar.gz")
		os.WriteFile(p, buf.Bytes(), 0o644)
		return p
	}
	extract := func(src, dest string) error {
		return exec.Command(py, helper, src, dest).Run()
	}

	root := t.TempDir()
	dest := filepath.Join(root, "out")
	src := archive(t,
		&tar.Header{Name: "./build/bin/llama-server", Typeflag: tar.TypeReg, Mode: 0o755},
		&tar.Header{Name: "build/bin/libllama.so.0.0.1", Typeflag: tar.TypeReg, Mode: 0o644},
		&tar.Header{Name: "build/bin/libllama.so.0", Typeflag: tar.TypeSymlink, Linkname: "libllama.so.0.0.1"},
		&tar.Header{Name: "build/bin/passwd", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		&tar.Header{Name: "build/bin/hard", Typeflag: tar.TypeLink, Linkname: "build/bin/llama-server"},
	)
	if err := extract(src, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "build", "bin", "llama-server")); err != nil || string(b) != "data of ./build/bin/llama-server" {
		t.Errorf("llama-server: %q %v", b, err)
	}
	if fi, err := os.Lstat(filepath.Join(dest, "build", "bin", "libllama.so.0")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("the library's link isn't a copy: %v", err)
	}
	for _, name := range []string{"passwd", "hard"} {
		if _, err := os.Lstat(filepath.Join(dest, "build", "bin", name)); err == nil {
			t.Errorf("%s was unpacked", name)
		}
	}

	for _, name := range []string{"../escape", "/abs/escape", "a/../../escape"} {
		src := archive(t, &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644})
		if err := extract(src, filepath.Join(root, "bad")); err == nil {
			t.Errorf("%s unpacked", name)
		}
		if _, err := os.Stat(filepath.Join(root, "escape")); err == nil {
			t.Fatalf("%s got out", name)
		}
	}
}
