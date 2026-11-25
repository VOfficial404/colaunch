package app

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

// Exporting.

func (a *App) openExport(sp *plugins.ScriptPlugin) {
	s := &a.tasks
	code, err := plugins.Export(sp).Code()
	if err != nil {
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("tasks.err_too_big"), Type: "error"})
		return
	}
	s.exporting, s.exportCode = sp, code
	s.view = taskExport
}

func (a *App) taskExportPage(c *ui.Context) {
	s := &a.tasks
	sp := s.exporting
	t := c.Theme()
	a.taskFrame(c, i18n.F("tasks.export_title", sp.Name().In(i18n.Lang())), func() {
		ui.Text(c, i18n.T("tasks.export_help")).FontSize(13).TextColor(t.TextMuted)
		section(c, i18n.T("tasks.code"), func() {
			code := s.exportCode
			ui.TextArea(c, &code).ReadOnly(true).Font("monospace").FontSize(12).Lines(3, 8)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if labeledButton(c, iconCopy, i18n.T("tasks.copy_code"), true).Clicked() {
					a.copyText(s.exportCode)
				}
				if labeledButton(c, iconFolder, i18n.T("tasks.save_file"), false).Clicked() {
					a.saveTaskFile(sp)
				}
			})
		})
		section(c, i18n.T("tasks.share_github"), func() {
			ui.Text(c, i18n.T("tasks.share_github_text")).FontSize(13).TextColor(t.TextMuted)
			ui.Row(c).Children(func() {
				if labeledButton(c, iconExternal, i18n.T("tasks.guide"), false).Clicked() {
					openURL(docsContribute)
				}
			})
		})
	}, nil)
}

// saveTaskFile has the user choose where the task's .colaunch.json goes:
// the save dialog on desktops, the system's picker on phones.
func (a *App) saveTaskFile(sp *plugins.ScriptPlugin) {
	pack := plugins.Export(sp)
	b, err := pack.JSON()
	if err != nil {
		return
	}
	dir, err := os.MkdirTemp("", "colaunch-export")
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, pack.FileName())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		os.RemoveAll(dir)
		return
	}
	go func() {
		defer os.RemoveAll(dir)
		to, err := mygo.Dialog.SaveCopy(tmp, pack.FileName())
		a.m.Post(func() {
			switch {
			case err != nil:
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("tasks.err_save"), Description: err.Error(), Type: "error"})
			case to != "":
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.F("tasks.saved_file", filepath.Base(to)), Type: "success", Timeout: 2e9})
			}
		})
	}()
}

// The community library.

func (a *App) loadIndex() {
	s := &a.tasks
	if s.indexLoading {
		return
	}
	s.indexLoading, s.indexErr = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		entries, err := plugins.FetchIndex(ctx, plugins.IndexURL)
		a.m.Post(func() {
			s.indexLoading = false
			if err != nil {
				s.indexErr = err.Error()
				return
			}
			s.index, s.indexLoaded = entries, true
		})
	}()
}

// fetchEntry downloads a task of the library, checks it against the
// index's checksum, and reviews it.
func (a *App) fetchEntry(e plugins.IndexEntry) {
	s := &a.tasks
	s.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p, err := plugins.FetchEntry(ctx, plugins.IndexURL, e)
		a.m.Post(func() {
			s.busy = false
			if err != nil {
				title, detail := taskError(err, "tasks.err_download")
				a.toasts = append(a.toasts, ui.Toast{Title: title, Description: detail, Type: "error"})
				return
			}
			if p.Author == "" {
				p.Author = e.Author
			}
			a.review(p)
		})
	}()
}

func (a *App) taskCommunityPage(c *ui.Context) {
	s := &a.tasks
	t := c.Theme()
	lang := i18n.Lang()
	a.taskFrame(c, i18n.T("tasks.community"), func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("tasks.community_help")).FontSize(13).TextColor(t.TextMuted).Grow(1).Shrink(1)
			if iconButton(c, iconRefresh, i18n.T("tasks.retry")).Disabled(s.indexLoading).Clicked() {
				a.loadIndex()
			}
		})
		switch {
		case s.indexLoading:
			ui.Text(c, i18n.T("tasks.loading")).FontSize(13).TextColor(t.TextMuted)
		case s.indexErr != "":
			ui.Column(c).Gap(6).Padding(10, 12).Radius(8).Border(1, t.Danger).Children(func() {
				ui.Text(c, i18n.T("tasks.err_index")).FontSize(13).TextColor(t.Danger)
				ui.Text(c, s.indexErr).Font("monospace").FontSize(12).TextColor(t.TextMuted).Selectable()
				ui.Row(c).Children(func() {
					if ui.Button(c, i18n.T("tasks.retry")).Clicked() {
						a.loadIndex()
					}
				})
			})
		case s.indexLoaded && len(s.index) == 0:
			ui.Text(c, i18n.T("tasks.community_empty")).FontSize(13).TextColor(t.TextMuted)
		}
		for _, e := range s.index {
			card(c.Key("entry-"+e.ID), func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Icon(c, iconPuzzle).TextColor(accent)
					ui.Text(c, e.Name.In(lang)).Bold().Grow(1).Shrink(1)
					if _, ok := a.userTask(plugins.Get(e.ID)); ok {
						ui.Badge(c, i18n.T("tasks.installed_badge"))
					}
				})
				if d := e.Description.In(lang); d != "" {
					ui.Text(c, d).FontSize(13).TextColor(t.TextMuted)
				}
				if e.Author != "" {
					ui.Text(c, i18n.F("tasks.by", e.Author)).FontSize(12).TextColor(t.TextMuted)
				}
				if len(e.Tags) > 0 {
					ui.Row(c).Gap(6).Wrap().Children(func() {
						for _, tag := range e.Tags {
							ui.Badge(c.Key("tag-"+tag), tag)
						}
					})
				}
				ui.Row(c).Children(func() {
					if labeledButton(c, iconImport, i18n.T("tasks.get"), false).Disabled(s.busy).Clicked() {
						a.fetchEntry(e)
					}
				})
			})
		}
	}, nil)
}
