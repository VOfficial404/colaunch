package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

// idConflict tells what has the ID id already: a built-in task, which
// can't be replaced, or one of the user's.
func (a *App) idConflict(id string) (builtin, user bool) {
	if p := plugins.Get(id); p != nil {
		if _, ok := a.userTask(p); ok {
			return false, true
		}
		return true, false
	}
	if _, err := os.Lstat(filepath.Join(a.m.PluginsDir(), id)); err == nil {
		return false, true
	}
	return false, false
}

func portKindName(k plugins.PortKind) string {
	switch k {
	case plugins.PortWeb:
		return i18n.T("tasks.kind_web")
	case plugins.PortOpenAI:
		return i18n.T("tasks.kind_openai")
	}
	return i18n.T("tasks.kind_tcp")
}

// taskReviewPage shows all a pack would do before it installs: there's
// no way to install one without it.
func (a *App) taskReviewPage(c *ui.Context) {
	s := &a.tasks
	p := s.pack
	m := p.Manifest
	t := c.Theme()
	lang := i18n.Lang()
	id := strings.TrimSpace(s.installID)
	builtin, user := a.idConflict(id)
	validID := plugins.ValidateTask(withID(m, id), p.Script) == nil
	a.taskFrame(c, i18n.T("tasks.review_title"), func() {
		card(c, func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconPuzzle).TextColor(accent)
				ui.Text(c, m.Name.In(lang)).Bold().FontSize(18).Grow(1).Shrink(1)
			})
			if d := m.Description.In(lang); d != "" {
				ui.Text(c, d).FontSize(13).TextColor(t.TextMuted)
			}
			author := p.Author
			if author == "" {
				author = i18n.T("tasks.not_given")
			}
			reviewRow(c, i18n.T("tasks.author"), author)
			reviewRow(c, i18n.T("tasks.source_label"), sourceText(p.Source))
			reviewRow(c, i18n.T("tasks.id"), m.ID)
		})

		ui.Column(c.Key("does")).Gap(8).Padding(14, 16).Radius(12).Border(1, t.Warning).Background(t.Warning.Alpha(0.06)).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconWarning).TextColor(t.Warning)
				ui.Text(c, i18n.T("tasks.does")).Bold().FontSize(15)
			})
			ui.Text(c, i18n.T("tasks.review_help")).FontSize(13).TextColor(t.TextMuted)
			doesLine(c, i18n.T("tasks.does_script"), t.Text, true)
			if len(m.Ports) == 0 {
				doesLine(c, i18n.T("tasks.does_no_ports"), t.Text, false)
			} else {
				doesLine(c, i18n.T("tasks.does_ports"), t.Text, true)
				for _, port := range m.Ports {
					ui.Text(c, i18n.F("tasks.port_line", port.Name, port.Remote, port.Local, portKindName(port.Kind))).Font("monospace").FontSize(12).PaddingX(18).Selectable()
				}
			}
			if m.HFToken {
				doesLine(c, i18n.T("tasks.does_hf"), t.Danger, true)
			} else {
				doesLine(c, i18n.T("tasks.does_no_hf"), t.TextMuted, false)
			}
			if v := variantName(m.Variant); v != "" {
				doesLine(c, i18n.F("tasks.does_hw", v), t.Text, false)
			} else {
				doesLine(c, i18n.T("tasks.does_no_hw"), t.TextMuted, false)
			}
			if len(m.Fields) > 0 {
				var names []string
				for _, f := range m.Fields {
					names = append(names, f.Label.In(lang))
				}
				doesLine(c, i18n.F("tasks.does_fields", strings.Join(names, ", ")), t.Text, false)
			}
			if m.Ready != "" {
				doesLine(c, i18n.T("tasks.does_ready"), t.Text, false)
				codeBox(c, m.Ready)
			}
		})

		if strings.TrimSpace(p.Readme) != "" {
			section(c, i18n.T("tasks.readme"), func() {
				ui.Text(c, strings.TrimSpace(p.Readme)).FontSize(13).Selectable()
			})
		}

		section(c, i18n.T("tasks.script"), func() {
			codeBox(c, p.Script)
			ui.Row(c).Children(func() {
				if labeledButton(c, iconCopy, i18n.T("tasks.copy_script"), false).Clicked() {
					a.copyText(p.Script)
				}
			})
		})

		if builtin || user || !validID || id != m.ID {
			card(c.Key("conflict"), func() {
				switch {
				case builtin:
					ui.Text(c, i18n.F("tasks.conflict_builtin", id)).FontSize(13).TextColor(t.Danger)
				case user:
					ui.Text(c, i18n.F("tasks.conflict_user", id)).FontSize(13).TextColor(t.Warning)
					switchRow(c, &s.replace, i18n.T("tasks.replace"))
				}
				ui.Field(c, i18n.T("tasks.install_id"), func() {
					ui.TextInput(c, &s.installID).Font("monospace").Autocorrect(false).Capitalize(ui.CapitalizeNone)
				})
				ui.Text(c, i18n.T("tasks.id_help")).FontSize(12).TextColor(t.TextMuted)
			})
		}
		if s.reviewErr != "" {
			ui.Text(c, s.reviewErr).FontSize(13).TextColor(t.Danger)
			if s.reviewDetail != "" {
				ui.Text(c, s.reviewDetail).Font("monospace").FontSize(12).TextColor(t.TextMuted).Selectable()
			}
		}
	}, func() {
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			if ui.Button(c, i18n.T("cancel")).Clicked() {
				s.pack = nil
				s.view = taskList
			}
			blocked := builtin || (user && !s.replace) || !validID
			if labeledButton(c, iconCheck, i18n.T("tasks.install"), true).Disabled(blocked).Clicked() {
				a.installReviewed()
			}
		})
	})
}

func withID(m plugins.Manifest, id string) plugins.Manifest {
	m.ID = id
	return m
}

func reviewRow(c *ui.Context, label, value string) {
	t := c.Theme()
	ui.Row(c.Key(label)).Gap(8).Wrap().Children(func() {
		ui.Text(c, label).FontSize(13).TextColor(t.TextMuted).Width(90)
		ui.Text(c, value).FontSize(13).Selectable().Grow(1).Shrink(1)
	})
}

// doesLine is one thing a task does, as a bullet.
func doesLine(c *ui.Context, text string, color ui.Color, bold bool) {
	ui.Row(c.Key(text)).Gap(8).Children(func() {
		ui.Text(c, "•").TextColor(color)
		line := ui.Text(c, text).FontSize(13).TextColor(color).Grow(1).Shrink(1)
		if bold {
			line.Bold()
		}
	})
}

// installReviewed installs the pack under review. It doesn't run it.
func (a *App) installReviewed() {
	s := &a.tasks
	p := *s.pack
	p.Manifest.ID = strings.TrimSpace(s.installID)
	if s.replace && a.taskInUse(p.Manifest.ID) {
		s.reviewErr, s.reviewDetail = i18n.T("tasks.in_use"), ""
		return
	}
	sp, err := plugins.Install(&p, a.m.PluginsDir(), s.replace)
	if err != nil {
		var conflict *plugins.ConflictError
		if errors.As(err, &conflict) {
			s.reviewErr, s.reviewDetail = "", ""
			return
		}
		s.reviewErr, s.reviewDetail = taskError(err, "tasks.err_save")
		return
	}
	a.m.ReloadPlugins()
	s.pack, s.code, s.link = nil, "", ""
	s.view = taskList
	a.toasts = append(a.toasts, ui.Toast{Title: i18n.F("tasks.installed", sp.Name().In(i18n.Lang())), Description: i18n.T("tasks.installed_text"), Type: "success"})
}
