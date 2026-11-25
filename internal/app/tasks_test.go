package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

func reviewPack() *plugins.Pack {
	return &plugins.Pack{
		Format: plugins.PackFormat, Version: plugins.PackVersion, Author: "Ada",
		Manifest: plugins.Manifest{
			ID:          "shared",
			Name:        plugins.Text{"en": "Shared task"},
			Description: plugins.Text{"en": "Serves a page."},
			Variant:     "VARIANT_GPU",
			HFToken:     true,
			Fields:      []plugins.Field{{Key: "model", Label: plugins.Text{"en": "Model"}, Kind: plugins.FieldText}},
			Ports:       []plugins.Port{{Name: "Web", Remote: 8000, Local: 8000, Kind: plugins.PortWeb, Path: "/"}},
			Ready:       "curl -sf http://127.0.0.1:8000/",
		},
		Script: "echo SHARED-SCRIPT\nexec python3 -m http.server 8000 --bind 127.0.0.1\n",
		Readme: "# Shared\n",
		Source: "code",
	}
}

// taskViews set the Tasks page up for each of its views, for
// TestPagesBuild: an installed task to edit and export, a pack to review
// whose ID is taken, the community library with an entry.
func taskViews(t *testing.T, a *App) map[string]func() {
	sp, err := plugins.Install(reviewPack(), a.m.PluginsDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	a.m.ReloadPlugins()
	t.Cleanup(func() { plugins.LoadDir(t.TempDir()) })
	entry := plugins.IndexEntry{ID: "comfyui", Name: plugins.Text{"en": "ComfyUI"}, Description: plugins.Text{"en": "Images"}, Author: "Ada", Tags: []string{"image"}}
	return map[string]func(){
		"list": func() { a.tasks.view, a.tasks.toDelete, a.tasks.deleteOpen = taskList, nil, false },
		"import": func() {
			a.tasks.view, a.tasks.importErr, a.tasks.importDetail = taskImport, i18n.T("tasks.err_invalid"), "the script is empty"
		},
		"review": func() { a.review(reviewPack()) },
		"review-builtin": func() {
			p := reviewPack()
			p.Manifest.ID = "llama"
			a.review(p)
		},
		"new":    func() { a.tasks.ed, a.tasks.view = newTaskEditor(nil, false), taskEdit },
		"edit":   func() { a.tasks.ed, a.tasks.view = newTaskEditor(sp, false), taskEdit },
		"export": func() { a.openExport(sp) },
		"community": func() {
			a.tasks.view, a.tasks.index, a.tasks.indexLoaded = taskCommunity, []plugins.IndexEntry{entry}, true
		},
		"delete": func() {
			a.tasks.view = taskList
			a.tasks.toDelete, a.tasks.deleteOpen = plugins.Get("shared").(*plugins.ScriptPlugin), true
		},
	}
}

func taskApp(t *testing.T) (*App, *ui.Tester) {
	i18n.SetLang("en")
	a := newTestApp(t)
	a.page = pagePlugins
	t.Cleanup(func() { plugins.LoadDir(t.TempDir()) })
	return a, ui.NewTester(a.view, 1120, 2400)
}

// A pasted share code goes to the review, which shows the script and what
// the task asks for, and installs nothing until its button is clicked;
// installing runs nothing.
func TestImportReviewsBeforeInstalling(t *testing.T) {
	a, tt := taskApp(t)
	code, err := reviewPack().Code()
	if err != nil {
		t.Fatal(err)
	}
	a.tasks.view, a.tasks.code = taskImport, code
	tt.Frame()
	if err := tt.Click("Read the code"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.tasks.view != taskReview || plugins.Get("shared") != nil {
		t.Fatalf("view %q, shared %v", a.tasks.view, plugins.Get("shared"))
	}
	for _, s := range []string{"SHARED-SCRIPT", "Gets your Hugging Face token, as HF_TOKEN.", "Web: runtime 8000 → 127.0.0.1:8000 (Web page)", "Ada", "a share code"} {
		if !tt.HasText(s) {
			t.Errorf("the review doesn't show %q", s)
		}
	}
	sessions := len(a.m.Sessions)
	if err := tt.Click("I've read the script, install"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	sp, ok := plugins.Get("shared").(*plugins.ScriptPlugin)
	if !ok || a.tasks.view != taskList || !sp.Meta.Imported() || sp.Meta.Source != "code" {
		t.Fatalf("view %q, shared %+v", a.tasks.view, plugins.Get("shared"))
	}
	if len(a.m.Sessions) != sessions {
		t.Error("installing launched the task")
	}
	if !tt.HasText("Imported") {
		t.Error("the list doesn't mark the task imported")
	}
}

// Cancelling the review installs nothing.
func TestReviewCancel(t *testing.T) {
	a, tt := taskApp(t)
	a.review(reviewPack())
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if plugins.Get("shared") != nil || a.tasks.view != taskList {
		t.Error("cancelling installed the task")
	}
}

// A built-in task's ID can't be installed over; another ID can.
func TestReviewBuiltinID(t *testing.T) {
	a, tt := taskApp(t)
	p := reviewPack()
	p.Manifest.ID = "llama"
	a.review(p)
	tt.Frame()
	tt.Click("I've read the script, install")
	tt.Frame()
	if _, ok := plugins.Get("llama").(*plugins.ScriptPlugin); ok || a.tasks.view != taskReview {
		t.Fatal("a task replaced the built-in one")
	}
	a.tasks.installID = "llama-two"
	tt.Frame()
	if err := tt.Click("I've read the script, install"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := plugins.Get("llama-two").(*plugins.ScriptPlugin); !ok {
		t.Errorf("not installed under another ID: %q %q", a.tasks.reviewErr, a.tasks.reviewDetail)
	}
}

// Installing again asks to replace the user's task of the same ID.
func TestReviewReplacesOnlyWhenAsked(t *testing.T) {
	a, tt := taskApp(t)
	if _, err := plugins.Install(reviewPack(), a.m.PluginsDir(), false); err != nil {
		t.Fatal(err)
	}
	a.m.ReloadPlugins()
	p := reviewPack()
	p.Script = "echo NEW\nexec sleep 1\n"
	a.review(p)
	tt.Frame()
	tt.Click("I've read the script, install")
	tt.Frame()
	if got := plugins.Get("shared").(*plugins.ScriptPlugin).Script(); got == p.Script {
		t.Fatal("replaced without asking")
	}
	if err := tt.Click("Replace my task"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("I've read the script, install"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := plugins.Get("shared").(*plugins.ScriptPlugin).Script(); got != p.Script {
		t.Errorf("script %q after replacing", got)
	}
}

// A new task gets its ID from its name, and saving loads it.
func TestEditorSavesNewTask(t *testing.T) {
	a, tt := taskApp(t)
	tt.Frame()
	if err := tt.Click("New task"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	e := a.tasks.ed
	if e == nil || a.tasks.view != taskEdit {
		t.Fatal("New task didn't open the editor")
	}
	e.names[0] = "My Web Server"
	e.id = plugins.SuggestID(e.firstName())
	e.fields = append(e.fields, &editField{n: 99, key: "mode", label: "Mode", kind: indexOf(editFieldKinds, plugins.FieldSelect), options: "fast, slow", def: "fast"})
	tt.Frame()
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	sp, ok := plugins.Get("my-web-server").(*plugins.ScriptPlugin)
	if !ok {
		t.Fatalf("not saved: %q %q", e.err, e.errDetail)
	}
	if sp.Meta.Imported() || len(sp.Manifest.Fields) != 1 || strings.Join(sp.Manifest.Fields[0].Options, "|") != "fast|slow" || sp.Manifest.Ports[0].Remote != 8000 {
		t.Errorf("saved %+v", sp.Manifest)
	}
	// A bad port is refused, and says why.
	a.tasks.ed = newTaskEditor(sp, false)
	a.tasks.view = taskEdit
	a.tasks.ed.ports[0].remote = 8080
	tt.Frame()
	tt.Click("Save")
	tt.Frame()
	if a.tasks.ed == nil || !strings.Contains(a.tasks.ed.errDetail, "8080") {
		t.Errorf("saving port 8080: %+v", a.tasks.ed)
	}
}

// Deleting asks first.
func TestDeleteAsks(t *testing.T) {
	a, tt := taskApp(t)
	if _, err := plugins.Install(reviewPack(), a.m.PluginsDir(), false); err != nil {
		t.Fatal(err)
	}
	a.m.ReloadPlugins()
	tt.Frame()
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.tasks.toDelete == nil || !tt.HasText("Delete Shared task?") || plugins.Get("shared") == nil {
		t.Fatalf("no confirmation: %q", tt.Texts())
	}
}

// colaunch://import links with a code open the review.
func TestDeepLinkOpensReview(t *testing.T) {
	a, _ := taskApp(t)
	a.page = pageDeploy
	code, _ := reviewPack().Code()
	a.openLink("colaunch://import?code=" + code)
	if a.page != pagePlugins || a.tasks.view != taskReview || a.tasks.pack == nil || a.tasks.pack.Manifest.ID != "shared" {
		t.Errorf("page %q view %q", a.page, a.tasks.view)
	}
	a.openLink("colaunch://import?url=http://example.com/x.json")
	if a.tasks.view != taskImport || a.tasks.importErr == "" {
		t.Errorf("an http link: view %q err %q", a.tasks.view, a.tasks.importErr)
	}
}
