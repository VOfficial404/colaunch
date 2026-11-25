package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
	"colaunch/tasks"
)

// The Tasks page shows one of these views: the list, importing, reviewing
// what's about to install, the editor, exporting, the community library.
const (
	taskList      = ""
	taskImport    = "import"
	taskReview    = "review"
	taskEdit      = "edit"
	taskExport    = "export"
	taskCommunity = "community"
)

const (
	docsTasks      = "https://github.com/VOfficial404/colaunch/blob/main/docs/plugins.md"
	docsContribute = "https://github.com/VOfficial404/colaunch/blob/main/tasks/README.md"
)

var (
	iconPencil  = lucide(`<path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/>`)
	iconShare   = lucide(`<path d="M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8"/><path d="m16 6-4-4-4 4"/><path d="M12 2v13"/>`)
	iconLibrary = lucide(`<path d="m16 6 4 14"/><path d="M12 6v14"/><path d="M8 8v12"/><path d="M4 4v16"/>`)
	iconWarning = lucide(`<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/>`)
)

type tasksState struct {
	view string

	// Importing.
	code, link   string
	importErr    string
	importDetail string
	busy         bool

	// Reviewing: the pack, and the ID it installs under.
	pack         *plugins.Pack
	installID    string
	replace      bool
	reviewErr    string
	reviewDetail string

	ed *taskEditor

	exporting  *plugins.ScriptPlugin
	exportCode string

	index        []plugins.IndexEntry
	indexErr     string
	indexLoaded  bool
	indexLoading bool

	toDelete   *plugins.ScriptPlugin
	deleteOpen bool

	// pendingLink is a colaunch:// link that came before the window.
	pendingLink string
}

// userTask returns the task of id when it's one of the plugins folder,
// which the user may edit, export and delete.
func (a *App) userTask(p plugins.Plugin) (*plugins.ScriptPlugin, bool) {
	sp, ok := p.(*plugins.ScriptPlugin)
	if !ok {
		return nil, false
	}
	rel, err := filepath.Rel(a.m.PluginsDir(), sp.Dir)
	return sp, err == nil && filepath.IsLocal(rel) && rel != "."
}

// taskInUse reports whether a runtime runs, or is starting, the task.
func (a *App) taskInUse(id string) bool {
	for _, s := range a.m.Live() {
		if s.Plugin == id {
			return true
		}
	}
	return false
}

// taskError explains what went wrong reading, fetching or installing a
// task: a sentence, and the details when there are some.
func taskError(err error, fallback string) (string, string) {
	switch {
	case errors.Is(err, plugins.ErrNotTask):
		return i18n.T("tasks.err_not_task"), ""
	case errors.Is(err, plugins.ErrNewerTask):
		return i18n.T("tasks.err_newer"), ""
	case errors.Is(err, plugins.ErrTooBig):
		return i18n.T("tasks.err_too_big"), ""
	case errors.Is(err, plugins.ErrChecksum):
		return i18n.T("tasks.err_checksum"), ""
	case errors.Is(err, plugins.ErrNotHTTPS):
		return i18n.T("tasks.err_https"), ""
	case errors.Is(err, plugins.ErrInvalid):
		return i18n.T("tasks.err_invalid"), strings.TrimPrefix(err.Error(), plugins.ErrInvalid.Error()+": ")
	}
	return i18n.T(fallback), err.Error()
}

// sourceText is where a task came from, to show.
func sourceText(src string) string {
	switch src {
	case "code":
		return i18n.T("tasks.from_code")
	case "":
		return i18n.T("tasks.from_editor")
	}
	return src
}

func variantName(v string) string {
	switch v {
	case "VARIANT_CPU":
		return "CPU"
	case "VARIANT_GPU":
		return "GPU"
	case "VARIANT_TPU":
		return "TPU"
	}
	return ""
}

func (a *App) tasksPage(c *ui.Context) {
	switch a.tasks.view {
	case taskImport:
		a.taskImportPage(c)
	case taskReview:
		if a.tasks.pack != nil {
			a.taskReviewPage(c)
			return
		}
		a.tasks.view = taskList
		a.taskListPage(c)
	case taskEdit:
		if a.tasks.ed != nil {
			a.taskEditorPage(c)
			return
		}
		a.tasks.view = taskList
		a.taskListPage(c)
	case taskExport:
		if a.tasks.exporting != nil {
			a.taskExportPage(c)
			return
		}
		a.tasks.view = taskList
		a.taskListPage(c)
	case taskCommunity:
		a.taskCommunityPage(c)
	default:
		a.taskListPage(c)
	}
}

// backToTasks is the way back from the Tasks page's views to its list.
func (a *App) backToTasks() func() {
	return func() { a.tasks.view = taskList }
}

// taskFrame is the frame of the Tasks page's views, with a button back
// to the list above the title.
func (a *App) taskFrame(c *ui.Context, title string, body func(), footer func()) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(10, pagePad(c), 0, pagePad(c)).Children(func() {
			if ui.Button(c, i18n.T("tasks.back")).Label(i18n.T("tasks.back_label")).Clicked() {
				a.tasks.view = taskList
			}
		})
		ui.Column(c).Grow(1).Children(func() {
			pageFrame(c, title, nil, nil, body, footer)
		})
	})
}

func (a *App) taskListPage(c *ui.Context) {
	s := &a.tasks
	t := c.Theme()
	var mine, builtin []plugins.Plugin
	for _, p := range plugins.All() {
		if _, ok := a.userTask(p); ok {
			mine = append(mine, p)
		} else {
			builtin = append(builtin, p)
		}
	}
	pageFrame(c, i18n.T("nav.tasks"), a.backToSettings(c), func() {
		if iconButton(c, iconRefresh, i18n.T("tasks.reload")).Clicked() {
			a.m.ReloadPlugins()
		}
	}, func() {
		ui.Text(c, i18n.T("tasks.help")).TextColor(t.TextMuted).FontSize(13)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if labeledButton(c, iconPlus, i18n.T("tasks.new"), true).Clicked() {
				s.ed = newTaskEditor(nil, false)
				s.view = taskEdit
			}
			if labeledButton(c, iconImport, i18n.T("tasks.import"), false).Clicked() {
				s.importErr, s.importDetail = "", ""
				s.view = taskImport
			}
			if labeledButton(c, iconLibrary, i18n.T("tasks.community"), false).Clicked() {
				s.view = taskCommunity
				if !s.indexLoaded {
					a.loadIndex()
				}
			}
		})
		section(c, i18n.T("tasks.mine"), func() {
			if len(mine) == 0 {
				ui.Text(c, i18n.T("tasks.none")).FontSize(13).TextColor(t.TextMuted)
			}
			for _, p := range mine {
				a.taskCard(c, p)
			}
		})
		section(c, i18n.T("tasks.builtin_title"), func() {
			for _, p := range builtin {
				a.taskCard(c, p)
			}
		})
		for _, err := range a.m.PluginErrors {
			ui.Text(c, err.Error()).TextColor(t.Danger).FontSize(12).Selectable()
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if isDesktop() {
				if labeledButton(c, iconFolder, i18n.T("tasks.open_folder"), false).Clicked() {
					dir := a.m.PluginsDir()
					a.writeExample(dir)
					mygo.Shell.OpenPath(dir)
				}
			}
			if labeledButton(c, iconExternal, i18n.T("tasks.docs"), false).Clicked() {
				openURL(docsTasks)
			}
		})
		a.copyable(c, i18n.T("plugins.folder"), a.m.PluginsDir())
	}, nil)
	if s.toDelete != nil {
		name := s.toDelete.Name().In(i18n.Lang())
		switch dangerConfirm(c, &s.deleteOpen, i18n.F("tasks.delete_title", name), i18n.T("tasks.delete_text"), i18n.T("cancel"), i18n.T("tasks.delete")) {
		case 1:
			a.deleteTask(s.toDelete)
			s.toDelete = nil
		case 0:
			s.toDelete = nil
		}
		if !s.deleteOpen {
			s.toDelete = nil
		}
	}
}

func (a *App) taskCard(c *ui.Context, p plugins.Plugin) {
	s := &a.tasks
	t := c.Theme()
	lang := i18n.Lang()
	sp, mine := a.userTask(p)
	card(c.Key("task-"+p.ID()), func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconPuzzle).TextColor(accent)
			ui.Text(c, p.Name().In(lang)).Bold().Grow(1).Shrink(1)
			switch {
			case mine && sp.Meta.Imported():
				ui.Badge(c, i18n.T("tasks.imported"))
			case mine:
				ui.Badge(c, i18n.T("plugins.user"))
			default:
				ui.Badge(c, i18n.T("plugins.builtin"))
			}
		})
		if d := p.Description().In(lang); d != "" {
			ui.Text(c, d).FontSize(13).TextColor(t.TextMuted)
		}
		if sp != nil && sp.Manifest.HFToken {
			ui.Text(c, i18n.T("plugins.hf_token")).FontSize(12).TextColor(t.Warning)
		}
		if mine && sp.Meta.Imported() {
			ui.Text(c, i18n.F("tasks.source", sourceText(sp.Meta.Source))).FontSize(12).TextColor(t.TextMuted).Selectable()
		}
		if sp != nil && sp.Meta.Author != "" {
			ui.Text(c, i18n.F("tasks.by", sp.Meta.Author)).FontSize(12).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			if labeledButton(c, iconRocket, i18n.T("tasks.use"), true).Clicked() {
				a.deploy.form.Plugin = p.ID()
				a.page = pageDeploy
			}
			switch {
			case mine:
				if labeledButton(c, iconPencil, i18n.T("tasks.edit"), false).Clicked() {
					s.ed = newTaskEditor(sp, false)
					s.view = taskEdit
				}
				if labeledButton(c, iconShare, i18n.T("tasks.export"), false).Clicked() {
					a.openExport(sp)
				}
				if labeledButton(c, iconTrash, i18n.T("tasks.delete"), false).Clicked() {
					if a.taskInUse(sp.ID()) {
						a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("tasks.in_use"), Type: "error"})
					} else {
						s.toDelete, s.deleteOpen = sp, true
					}
				}
			case sp != nil:
				// A task of a script, but not of the plugins folder.
				if labeledButton(c, iconCopy, i18n.T("tasks.duplicate"), false).Clicked() {
					s.ed = newTaskEditor(sp, true)
					s.view = taskEdit
				}
			default:
				ui.Text(c, i18n.T("tasks.builtin_locked")).FontSize(12).TextColor(t.TextMuted)
			}
		})
	})
}

func (a *App) deleteTask(sp *plugins.ScriptPlugin) {
	if a.taskInUse(sp.ID()) {
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("tasks.in_use"), Type: "error"})
		return
	}
	if err := plugins.Remove(a.m.PluginsDir(), sp); err != nil {
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("tasks.err_save"), Description: err.Error(), Type: "error"})
		return
	}
	a.m.ReloadPlugins()
	a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("tasks.deleted"), Type: "success", Timeout: 2e9})
}

// writeExample puts the example task, JupyterLab, in the plugins folder
// when it has none, to start from.
func (a *App) writeExample(dir string) {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return
	}
	if _, err := plugins.Install(tasks.Example(), dir, false); err == nil {
		a.m.ReloadPlugins()
	}
}

// Importing.

func (a *App) taskImportPage(c *ui.Context) {
	s := &a.tasks
	t := c.Theme()
	a.taskFrame(c, i18n.T("tasks.import_title"), func() {
		ui.Text(c, i18n.T("tasks.import_help")).FontSize(13).TextColor(t.TextMuted)
		if s.importErr != "" {
			ui.Column(c).Gap(4).Padding(10, 12).Radius(8).Border(1, t.Danger).Children(func() {
				ui.Text(c, s.importErr).FontSize(13).TextColor(t.Danger)
				if s.importDetail != "" {
					ui.Text(c, s.importDetail).Font("monospace").FontSize(12).TextColor(t.TextMuted).Selectable()
				}
			})
		}
		section(c, i18n.T("tasks.code"), func() {
			ui.TextArea(c, &s.code).Placeholder(plugins.CodePrefix+"H4sI…").Font("monospace").Lines(3, 6)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if labeledButton(c, iconImport, i18n.T("tasks.read_code"), true).Disabled(strings.TrimSpace(s.code) == "").Clicked() {
					a.readCode(s.code)
				}
				if ui.Button(c, i18n.T("tasks.paste")).Clicked() {
					if text := mygo.Clipboard.ReadText(); text != "" {
						s.code = text
					}
				}
			})
		})
		section(c, i18n.T("tasks.file"), func() {
			ui.Row(c).Children(func() {
				if labeledButton(c, iconFolder, i18n.T("tasks.open_file"), false).Disabled(s.busy).Clicked() {
					a.openTaskFile()
				}
			})
		})
		section(c, i18n.T("tasks.link"), func() {
			ui.TextInput(c, &s.link).Placeholder(example("https://github.com/…/task.colaunch.json")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			ui.Text(c, i18n.T("tasks.link_help")).FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if labeledButton(c, iconGlobe, i18n.T("tasks.fetch"), false).Disabled(s.busy || strings.TrimSpace(s.link) == "").Clicked() {
					a.fetchTask(s.link)
				}
				if s.busy {
					ui.Text(c, i18n.T("tasks.fetching")).FontSize(12).TextColor(t.TextMuted)
				}
			})
		})
	}, nil)
}

func (a *App) importFailed(err error, fallback string) {
	s := &a.tasks
	s.importErr, s.importDetail = taskError(err, fallback)
	s.view = taskImport
}

// readCode reads a pasted share code, or a pack's JSON, and reviews it.
func (a *App) readCode(code string) {
	p, err := plugins.Parse([]byte(code))
	if err != nil {
		a.importFailed(err, "tasks.err_not_task")
		return
	}
	p.Source = "code"
	a.review(p)
}

// openTaskFile has the user choose a .colaunch.json: the open dialog on
// desktops, the system's picker on phones, which gives a copy.
func (a *App) openTaskFile() {
	win := a.win
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:  win,
			Title:   i18n.T("tasks.open_file"),
			Filters: []mygo.FileFilter{{Name: "Colaunch", Extensions: []string{"json"}}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		b, err := readSmall(paths[0], plugins.MaxPack)
		name := filepath.Base(paths[0])
		a.m.Post(func() {
			if err != nil {
				a.importFailed(err, "tasks.err_file")
				return
			}
			p, err := plugins.Parse(b)
			if err != nil {
				a.importFailed(err, "tasks.err_file")
				return
			}
			p.Source = name
			a.review(p)
		})
	}()
}

// readSmall reads at most limit bytes of a file, and ErrTooBig past them.
func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, plugins.ErrTooBig
	}
	return b, nil
}

// fetchTask downloads the pack of an https link and reviews it.
func (a *App) fetchTask(link string) {
	s := &a.tasks
	s.busy, s.importErr, s.importDetail = true, "", ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p, err := plugins.FetchPack(ctx, link)
		a.m.Post(func() {
			s.busy = false
			if err != nil {
				a.importFailed(err, "tasks.err_download")
				return
			}
			a.review(p)
		})
	}()
}

// review shows a pack before it installs, which nothing skips.
func (a *App) review(p *plugins.Pack) {
	s := &a.tasks
	s.pack, s.installID, s.replace = p, p.Manifest.ID, false
	s.reviewErr, s.reviewDetail = "", ""
	s.importErr, s.importDetail = "", ""
	s.view = taskReview
	a.page = pagePlugins
}

// openLink handles a colaunch://import link: its code goes to the review,
// its https link is downloaded first.
func (a *App) openLink(link string) {
	s := &a.tasks
	a.page = pagePlugins
	code, packURL, err := plugins.ParseDeepLink(link)
	switch {
	case err != nil:
		s.importErr, s.importDetail = i18n.T("tasks.err_link"), err.Error()
		s.view = taskImport
	case code != "":
		s.code = code
		a.readCode(code)
	default:
		s.link = packURL
		s.view = taskImport
		a.fetchTask(packURL)
	}
}

// onOpenURL takes the colaunch:// links the system opens the app with.
func (a *App) onOpenURL(link string) {
	if a.win == nil {
		a.tasks.pendingLink = link
		return
	}
	a.win.Update(func() {
		a.openLink(link)
		if isDesktop() {
			a.showDesktopWindow()
		}
	})
}
