package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"colaunch/internal/plugins"
)

// Every entry of index.json names a pack of this folder with its SHA-256,
// and every pack is listed, valid, and has the entry's ID.
func TestIndexMatchesPacks(t *testing.T) {
	b, err := os.ReadFile("index.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := plugins.ParseIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	packs, _ := filepath.Glob("*" + plugins.PackExt)
	if len(entries) != len(packs) || len(packs) == 0 {
		t.Fatalf("%d entries for %d packs: every entry needs an id, a path and a sha256", len(entries), len(packs))
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.FromSlash(e.Path))
		if err != nil {
			t.Errorf("%s: %v", e.ID, err)
			continue
		}
		if err := plugins.CheckSHA256(body, e.SHA256); err != nil {
			t.Errorf("%s: index.json's sha256 isn't that of %s: run sha256sum on it", e.ID, e.Path)
		}
		p, err := plugins.Parse(body)
		if err != nil {
			t.Errorf("%s: %v", e.Path, err)
			continue
		}
		if p.Manifest.ID != e.ID {
			t.Errorf("%s: the pack's id is %q, the index's %q", e.Path, p.Manifest.ID, e.ID)
		}
		if e.Name.In("en") == "" || e.Author == "" {
			t.Errorf("%s: the entry needs a name and an author", e.ID)
		}
		for _, port := range p.Manifest.Ports {
			if !strings.Contains(p.Script, "127.0.0.1") {
				t.Errorf("%s: the server should listen on 127.0.0.1 (port %d)", e.ID, port.Remote)
			}
		}
		if !strings.Contains(p.Script, "exec ") {
			t.Errorf("%s: the script should exec its server", e.ID)
		}
	}
	if Example().Manifest.ID != "jupyter-lab" {
		t.Error("the example isn't JupyterLab")
	}
}

// The packs' scripts are bash that parses.
func TestScriptsParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	if out, err := exec.Command(bash, "-c", "true").CombinedOutput(); err != nil {
		t.Skipf("bash doesn't run here: %v %s", err, out)
	}
	packs, _ := filepath.Glob("*" + plugins.PackExt)
	for _, path := range packs {
		b, _ := os.ReadFile(path)
		p, err := plugins.Parse(b)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(p.Script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n: %v\n%s", path, err, out)
		}
	}
}
