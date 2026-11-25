package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/core"
	"colaunch/internal/i18n"
)

type portsState struct {
	session string
	remote  float64
	local   float64
	name    string
}

func (a *App) portsPage(c *ui.Context) {
	p := &a.ports
	t := c.Theme()
	pageScroll(c, i18n.T("nav.ports"), nil, func() {
		ui.Text(c, i18n.T("ports.help")).TextColor(t.TextMuted).FontSize(13)
		var running []*core.Session
		for _, s := range a.m.Live() {
			if s.Phase == core.PhaseRunning {
				running = append(running, s)
			}
		}
		if len(running) == 0 {
			card(c, func() { ui.Text(c, i18n.T("ports.none")).TextColor(t.TextMuted) })
			return
		}
		for _, s := range running {
			section(c.Key(s.ID), s.Title()+" · "+s.Spec.String(), func() {
				if len(s.Forwards) == 0 {
					ui.Text(c, i18n.T("ports.empty")).TextColor(t.TextMuted).FontSize(13)
				}
				for _, f := range s.Forwards {
					ui.Row(c.Key(f)).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
						ui.Text(c, f.Port.Name).Bold().Width(140).SingleLine().Ellipsis("…")
						if f.Err != "" {
							ui.Text(c, f.Err).TextColor(t.Danger).Grow(1)
						} else {
							ui.Text(c, fmt.Sprintf("%s  →  runtime:%d", f.Address(), f.Port.Remote)).Font("monospace").FontSize(13).Selectable().Grow(1)
							ui.Text(c, i18n.F("ports.conns", f.Conns())).FontSize(12).TextColor(t.TextMuted)
							if iconButton(c, iconCopy, i18n.T("copy")).Clicked() {
								a.copyText(f.Address())
							}
							if iconButton(c, iconExternal, i18n.T("open")).Clicked() {
								openURL(f.URL())
							}
						}
						if f.Extra {
							if iconButton(c, iconTrash, i18n.T("remove")).Clicked() {
								a.m.RemoveForward(s, f)
							}
						}
					})
				}
			})
		}
		section(c, i18n.T("ports.add"), func() {
			names := make([]string, len(running))
			chosen := ""
			for i, s := range running {
				names[i] = fmt.Sprintf("%s · %s", s.Title(), s.Spec)
				if s.ID == p.session {
					chosen = names[i]
				}
			}
			if chosen == "" {
				chosen, p.session = names[0], running[0].ID
			}
			ui.Field(c, i18n.T("ports.runtime"), func() {
				if ui.Select(c, &chosen, names).Changed() {
					for i, n := range names {
						if n == chosen {
							p.session = running[i].ID
						}
					}
				}
			})
			ui.Grid(c).Columns(2).GapX(16).GapY(12).Children(func() {
				ui.Field(c, i18n.T("ports.remote"), func() { ui.NumberInput(c, &p.remote, 1, 65535, 1) })
				ui.Field(c, i18n.T("ports.local"), func() { ui.NumberInput(c, &p.local, 0, 65535, 1) })
			})
			ui.Field(c, i18n.T("ports.name"), func() { ui.TextInput(c, &p.name).Placeholder("TensorBoard") })
			ui.Text(c, i18n.T("ports.local_help")).FontSize(12).TextColor(t.TextMuted)
			if labeledButton(c, iconPlus, i18n.T("ports.add"), true).Disabled(p.remote < 1).Clicked() {
				if s := a.m.Session(p.session); s != nil {
					local := int(p.local)
					if local == 0 {
						local = int(p.remote)
					}
					a.m.AddForward(s, int(p.remote), local, p.name)
					p.remote, p.local, p.name = 0, 0, ""
				}
			}
		})
	})
}
