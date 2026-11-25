package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"colaunch/internal/plugins"
)

func fileHas(t *testing.T, path, s string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), s)
}

// Password fields, as the llama plugin's API key, stay out of
// settings.json and state.json, and come back from Secrets.
func TestPasswordsStayOutOfFiles(t *testing.T) {
	dir, sec := t.TempDir(), mapSecrets{}
	const key = "sk-with spaces"
	m := New(dir, sec)
	m.Settings.Deploy.Values["llama"] = map[string]string{"repo": "a/b-GGUF", "api_key": key}
	m.SaveSettings()
	s := &Session{Phase: PhaseRunning, sessionRecord: sessionRecord{
		ID: "s1", Plugin: "llama",
		Values: map[string]string{"repo": "a/b-GGUF", "api_key": key},
		Result: &plugins.Result{Model: "b", APIKey: key},
	}}
	m.Sessions = []*Session{s}
	m.saveState()
	m.Close()
	for _, f := range []string{"settings.json", "state.json"} {
		if fileHas(t, filepath.Join(dir, f), key) {
			t.Errorf("%s has the API key", f)
		}
	}
	if !fileHas(t, filepath.Join(dir, "settings.json"), "a/b-GGUF") {
		t.Error("settings.json lost the other values")
	}

	m = New(dir, sec)
	defer m.Close()
	if got := m.Settings.Deploy.Values["llama"]["api_key"]; got != key {
		t.Errorf("the settings' API key came back as %q", got)
	}
	if len(m.Sessions) != 1 || m.Sessions[0].Values["api_key"] != key || m.Sessions[0].Result.APIKey != key {
		t.Fatalf("the session came back as %+v", m.Sessions)
	}
	if m.Sessions[0].Values["repo"] != "a/b-GGUF" {
		t.Error("the session lost its other values")
	}

	// A session that ended takes its secrets with it.
	m.Sessions[0].Phase = PhaseStopped
	m.saveState()
	if _, ok := sec[sessionSecretKey("s1")]; ok {
		t.Error("an ended session's secrets stayed")
	}
	// Emptying the field forgets it.
	m.Settings.Deploy.Values["llama"]["api_key"] = ""
	m.SaveSettings()
	if _, ok := sec[settingSecretKey("llama", "api_key")]; ok {
		t.Error("an emptied API key stayed")
	}
}

// Files from before keep their passwords in the open: loading them moves
// those into Secrets and writes the files again without.
func TestPasswordsInOldFilesMove(t *testing.T) {
	dir, sec := t.TempDir(), mapSecrets{}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"deploy": {"plugin": "llama", "values": {"llama": {"repo": "a/b", "api_key": "old-key"}}}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"sessions": [{"id": "s9", "plugin": "llama", "values": {"api_key": "old-key"}, "result": {"Model": "b", "APIKey": "old-key"}}]}`), 0o600)
	m := New(dir, sec)
	defer m.Close()
	for _, f := range []string{"settings.json", "state.json"} {
		if fileHas(t, filepath.Join(dir, f), "old-key") {
			t.Errorf("%s still has the API key", f)
		}
	}
	if m.Settings.Deploy.Values["llama"]["api_key"] != "old-key" || m.Sessions[0].Values["api_key"] != "old-key" || m.Sessions[0].Result.APIKey != "old-key" {
		t.Error("the API key was lost moving it")
	}
	if string(sec[settingSecretKey("llama", "api_key")]) != "old-key" || len(sec[sessionSecretKey("s9")]) == 0 {
		t.Errorf("Secrets has %v", sec)
	}
}
