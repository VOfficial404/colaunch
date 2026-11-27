package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"colaunch/internal/comfy"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
)

func TestCreateImageGeneratesOnlyOnSelectedRuntime(t *testing.T) {
	fc := newFakeComfy(t, false)
	other := newFakeComfy(t, false)
	a, s, f, posted := createTestApp(t, fc)
	otherSession := addComfy(a, "f6", f)
	a.create.selectSession(s.ID)
	a.create.addr = func(session *core.Session) string {
		if session == otherSession {
			return strings.TrimPrefix(other.URL, "http://")
		}
		return strings.TrimPrefix(fc.URL, "http://")
	}
	tt := ui.NewTester(a.view, 1120, 1600)
	a.create.init()
	a.create.form(f).prompt = "a mountain lake at dawn"
	original := testPNG(640, 320)
	filename := writeCreateInputTestFile(t, "lake.png", original)
	a.create.pickInputFiles = func(mygo.OpenDialogOptions) ([]string, error) { return []string{filename}, nil }
	pickCreateTestImage(t, a, tt, posted, "create.input_choose")
	input := a.create.input
	if input.image == nil || input.image.Filename != "lake.png" || tt.HasText(filename) {
		t.Fatal("the selection should show its basename only")
	}
	if w, h := ui.ImageSize(input.preview); w != 192 || h != 96 {
		t.Fatalf("preview size = %v×%v", w, h)
	}
	if input.denoise != comfy.DefaultDenoise {
		t.Fatalf("default redraw strength = %v", input.denoise)
	}
	if err := tt.Click(i18n.T("create.input_denoise")); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyUp)
	if input.denoise != 0.6 {
		t.Fatalf("edited redraw strength = %v, want 0.6", input.denoise)
	}
	fc.mu.Lock()
	uploadsBeforeGenerate := len(fc.uploads)
	fc.mu.Unlock()
	if uploadsBeforeGenerate != 0 {
		t.Fatal("selecting the image uploaded it before Generate")
	}
	if err := tt.Click(i18n.T("create.generate")); err != nil {
		t.Fatal(err)
	}
	if a.create.jobs[s.ID] == nil {
		t.Fatal("the selected runtime didn't start generating")
	}
	pump(t, posted, func() bool { return a.create.jobs[s.ID] == nil })
	if len(a.create.items) != 1 || !a.create.items[0].usedInput || a.create.errs[s.ID] != "" {
		t.Fatalf("image run: %d results, error %q", len(a.create.items), a.create.errs[s.ID])
	}
	fc.mu.Lock()
	uploads, prompts := fc.uploads, fc.prompts
	fc.mu.Unlock()
	if len(uploads) != 1 || !bytes.Equal(uploads[0].Data, original) || uploads[0].Filename == "lake.png" {
		t.Fatal("expected one upload of the original bytes under a generated name")
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], fmt.Sprintf(`"image":%q`, uploads[0].Filename)) || !strings.Contains(prompts[0], `"denoise":0.6`) {
		t.Fatalf("expected the selected image and strength in the workflow: %v", prompts)
	}
	other.mu.Lock()
	otherRequests := len(other.uploads) + len(other.prompts)
	other.mu.Unlock()
	if otherRequests != 0 {
		t.Fatal("sent the image or prompt to a different runtime")
	}
	tt.Frame()
	if _, ok := tt.Find(i18n.T("create.again")); ok {
		t.Fatal("image results must not offer one-click replay")
	}
	a.again(s, a.create.items[0])
	if a.generating() != 0 {
		t.Fatal("replayed an image result without its original")
	}
	if err := tt.Click(i18n.T("create.input_remove")); err != nil {
		t.Fatal(err)
	}
	if input.image != nil || input.preview != nil {
		t.Fatal("Remove kept the image or its preview")
	}
	if err := tt.Click(i18n.T("create.generate")); err != nil {
		t.Fatal(err)
	}
	pump(t, posted, func() bool { return a.create.jobs[s.ID] == nil })
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.uploads) != 1 || len(fc.prompts) != 2 || strings.Contains(fc.prompts[1], `"LoadImage"`) || a.create.items[0].usedInput {
		t.Fatal("removing the image didn't restore text-only generation")
	}
}

func TestCreateInputReplacementAndRuntimeChange(t *testing.T) {
	a, s, f, posted := createTestApp(t, newFakeComfy(t, false))
	other := addComfy(a, "f6", f)
	a.create.selectSession(s.ID)
	tt := ui.NewTester(a.view, 390, 1500)
	first := writeCreateInputTestFile(t, "first.png", testPNG(8, 8))
	second := writeCreateInputTestFile(t, "second.png", testPNG(8, 8))
	invalid := writeCreateInputTestFile(t, "invalid.png", []byte("not an image"))
	steps := []struct {
		path, action, name, errKey string
	}{
		{first, "create.input_choose", "first.png", ""},
		{invalid, "create.input_replace", "first.png", "create.input_file_err"},
		{"", "create.input_replace", "first.png", ""},
		{second, "create.input_replace", "second.png", ""},
	}
	for _, step := range steps {
		a.create.pickInputFiles = func(mygo.OpenDialogOptions) ([]string, error) {
			if step.path == "" {
				return nil, mygo.ErrCanceled
			}
			return []string{step.path}, nil
		}
		pickCreateTestImage(t, a, tt, posted, step.action)
		if input := a.create.input; input.image == nil || input.image.Filename != step.name || input.err != step.errKey {
			t.Fatalf("%s %q: wrong selection or error %q", step.action, step.name, input.err)
		}
		if step.path != "" && tt.HasText(step.path) {
			t.Fatal("the picker leaked the full file path")
		}
	}
	a.create.pickInputFiles = func(mygo.OpenDialogOptions) ([]string, error) { return []string{first}, nil }
	if err := tt.Click(i18n.T("create.input_replace")); err != nil {
		t.Fatal(err)
	}
	label := func(s *core.Session) string { return fmt.Sprintf("%s · %s · %s", shortTitle(s), s.Spec, s.ID) }
	if err := tt.Click(label(s)); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(label(other)); err != nil {
		t.Fatal(err)
	}
	pump(t, posted, func() bool { return a.create.inputPicker == nil })
	tt.Frame()
	if input := a.create.input; input == nil || input.session != other.ID || input.image != nil {
		t.Fatal("a late picker result followed the user to another runtime")
	}
	a.create.selectSession(s.ID)
	tt.Frame()
	if a.create.input.image != nil {
		t.Fatal("the unselected runtime retained its original image")
	}
}

func TestCreateWanFirstFrame(t *testing.T) {
	fc := newFakeComfy(t, false)
	a, s, _, posted := createTestApp(t, fc)
	f := comfy.FamilyByID("wan22-ti2v-5b")
	s.Result.Info[0][1] = f.ID
	tt := ui.NewTester(a.view, 390, 1500)
	tt.Frame()
	a.create.form(f).prompt = "the waves roll ashore"
	filename := writeCreateInputTestFile(t, "waves.png", testPNG(16, 8))
	a.create.pickInputFiles = func(mygo.OpenDialogOptions) ([]string, error) { return []string{filename}, nil }
	pickCreateTestImage(t, a, tt, posted, "create.input_choose")
	if !tt.HasText(i18n.T("create.input_first_frame")) || tt.HasText(i18n.T("create.input_denoise")) {
		t.Fatal("Wan 2.2 should describe the image as its first frame")
	}
	if err := tt.Click(i18n.T("create.generate")); err != nil {
		t.Fatal(err)
	}
	pump(t, posted, func() bool { return a.create.jobs[s.ID] == nil })
	fc.mu.Lock()
	prompts := fc.prompts
	fc.mu.Unlock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], `"start_image":`) {
		t.Fatalf("Wan 2.2 didn't use the image as its first frame: %v, error %q", prompts, a.create.errs[s.ID])
	}
	s.Result.Info[0][1] = "wan21-t2v"
	tt.Frame()
	if a.create.input != nil {
		t.Fatal("a text-only family retained an input selection")
	}
	if _, ok := tt.Find(i18n.T("create.input_choose")); ok {
		t.Fatal("a text-only family offered image selection")
	}
}

func writeCreateInputTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func pickCreateTestImage(t *testing.T, a *App, tt *ui.Tester, posted chan func(), action string) {
	t.Helper()
	if err := tt.Click(i18n.T(action)); err != nil {
		t.Fatal(err)
	}
	if a.create.inputPicker == nil {
		t.Fatal("image picker didn't open")
	}
	pump(t, posted, func() bool { return a.create.inputPicker == nil })
	tt.Frame()
}
