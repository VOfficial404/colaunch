package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/core"
	"colaunch/internal/i18n"
)

type portsState struct {
	session string
	// remote and local are the ports as typed.
	remote string
	local  string
	name   string
	// tried is set once Forward was clicked, to say what's wrong.
	tried bool
	// hidden are forwards removed, until the toast that can undo it
	// closes.
	hidden map[*core.ForwardState]bool
}

// parsePort reads a port number typed into a field: ok false when it
// isn't one from lo to 65535. Empty is 0, which only lo 0 takes.
func parsePort(s string, lo int) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, lo == 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > 65535 {
		return 0, false
	}
	return n, true
}

// backToSettings is the way back from the pages phones reach through
// Settings, which have no tab of their own.
func (a *App) backToSettings(c *ui.Context) func() {
	if !isNarrow(c) {
		return nil
	}
	return func() { a.page = pageSettings }
}

func (a *App) portsPage(c *ui.Context) {
	p := &a.ports
	if p.hidden == nil {
		p.hidden = map[*core.ForwardState]bool{}
	}
	t := c.Theme()
	pageFrame(c, i18n.T("nav.ports"), a.backToSettings(c), nil, func() {
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
			section(c.Key(s.ID), shortTitle(s)+" · "+s.Spec.String(), func() {
				shown := 0
				for _, f := range s.Forwards {
					if p.hidden[f] {
						continue
					}
					shown++
					ui.Row(c.Key(f)).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
						ui.Text(c, f.Port.Name).Bold().Width(140).SingleLine().Ellipsis("…")
						if f.Err != "" {
							ui.Text(c, f.Err).TextColor(t.Danger).Grow(1)
						} else {
							ui.Text(c, i18n.F("ports.mapping", f.Address(), f.Port.Remote)).Font("monospace").FontSize(13).Selectable().Grow(1)
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
								id, fw := s.ID, f
								a.undoable(i18n.T("ports.removed"),
									func() { p.hidden[fw] = true },
									func() { delete(p.hidden, fw) },
									func() {
										delete(p.hidden, fw)
										if s := a.m.Session(id); s != nil {
											a.m.RemoveForward(s, fw)
										}
									})
							}
						}
					})
				}
				if shown == 0 {
					ui.Text(c, i18n.T("ports.empty")).TextColor(t.TextMuted).FontSize(13)
				}
			})
		}
		section(c, i18n.T("ports.add"), func() {
			names := make([]string, len(running))
			chosen := ""
			for i, s := range running {
				names[i] = fmt.Sprintf("%s · %s", shortTitle(s), s.Spec)
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
			remote, remoteOK := parsePort(p.remote, 1)
			local, localOK := parsePort(p.local, 0)
			remoteErr, localErr := "", ""
			if (p.tried || p.remote != "") && !remoteOK {
				remoteErr = i18n.T("ports.bad_remote")
			}
			if !localOK {
				localErr = i18n.T("ports.bad_local")
			}
			ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(12).Children(func() {
				ui.Field(c, fieldLabel(i18n.T("ports.remote"), true), func() {
					ui.TextInput(c, &p.remote).Keyboard(ui.KeyboardNumber).Placeholder(example("6006")).MaxLength(5)
				}).Error(remoteErr)
				ui.Field(c, i18n.T("ports.local"), func() {
					ui.TextInput(c, &p.local).Keyboard(ui.KeyboardNumber).Placeholder(example("0")).MaxLength(5)
				}).Error(localErr)
			})
			ui.Field(c, i18n.T("ports.name"), func() { ui.TextInput(c, &p.name).Placeholder(example("TensorBoard")) })
			ui.Text(c, i18n.T("ports.local_help")).FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, i18n.T("form.required_note")).FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Children(func() {
				ui.Spacer(c)
				if labeledButton(c, iconPlus, i18n.T("ports.add"), true).Clicked() {
					p.tried = true
					if !remoteOK || !localOK {
						return
					}
					if s := a.m.Session(p.session); s != nil {
						if local == 0 {
							local = remote
						}
						a.m.AddForward(s, remote, local, p.name)
						p.remote, p.local, p.name, p.tried = "", "", "", false
					}
				}
			})
		})
	}, nil)
}
