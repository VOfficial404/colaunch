package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/core"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

type runtimesState struct {
	selected   string
	confirm    bool
	toStop     *core.Session
	showLog    map[string]bool
	remoteLog  map[string]string
	editGuards map[string]*core.Guards
	release    *core.UnmanagedRuntime
	confirmRel bool
}

func (a *App) runtimesPage(c *ui.Context) {
	r := &a.runtimes
	if r.showLog == nil {
		r.showLog, r.remoteLog, r.editGuards = map[string]bool{}, map[string]string{}, map[string]*core.Guards{}
	}
	t := c.Theme()
	pageScroll(c, i18n.T("nav.runtimes"), func() {
		if iconButton(c, iconRefresh, i18n.T("refresh")).Clicked() {
			for _, acct := range a.m.Accounts {
				a.m.Refresh(acct)
			}
		}
	}, func() {
		live := a.m.Live()
		unmanaged := a.m.Unmanaged()
		if len(live) == 0 && len(unmanaged) == 0 {
			card(c, func() {
				ui.Text(c, i18n.T("runtimes.empty")).TextColor(t.TextMuted)
				if labeledButton(c, iconRocket, i18n.T("nav.deploy"), true).Clicked() {
					a.page = pageDeploy
				}
			})
		}
		for _, s := range live {
			ui.Column(c.Key(s.ID)).Children(func() { a.sessionCard(c, s) })
		}
		if len(unmanaged) > 0 {
			section(c, i18n.T("runtimes.unmanaged"), func() {
				ui.Text(c, i18n.T("runtimes.unmanaged_help")).FontSize(12).TextColor(t.TextMuted)
				for _, u := range unmanaged {
					ui.Row(c.Key(u.Runtime.Name)).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, u.Runtime.Spec.String()).Bold()
						ui.Text(c, u.Account.Email).TextColor(t.TextMuted).FontSize(12).Grow(1)
						if labeledButton(c, iconStop, i18n.T("stop"), false).Clicked() {
							rel := u
							r.release, r.confirmRel = &rel, true
						}
					})
				}
			})
		}
		var ended []*core.Session
		for _, s := range a.m.Sessions {
			if s.Ended() {
				ended = append(ended, s)
			}
		}
		if len(ended) > 0 {
			section(c, i18n.T("runtimes.history"), func() {
				for i := len(ended) - 1; i >= 0; i-- {
					s := ended[i]
					ui.Column(c.Key("h" + s.ID)).Gap(4).Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, s.Title()).Bold().Grow(1).Shrink(1).SingleLine().Ellipsis("…")
							phaseBadge(c, s)
							if iconButton(c, iconLog, i18n.T("runtimes.log")).Clicked() {
								r.showLog[s.ID] = !r.showLog[s.ID]
							}
							if iconButton(c, iconTrash, i18n.T("remove")).Clicked() {
								a.m.Forget(s)
							}
						})
						ui.Text(c, fmt.Sprintf("%s · %s · %s", s.Spec, duration(s.Uptime()), stopReason(s))).FontSize(12).TextColor(t.TextMuted)
						if s.Err != "" {
							ui.Text(c, s.Err).FontSize(12).TextColor(t.Danger).Selectable()
						}
						if r.showLog[s.ID] {
							logBox(c, s)
						}
					})
				}
			})
		}
	})
	if r.confirm && r.toStop != nil {
		s := r.toStop
		switch ui.AlertDialog(c, &r.confirm, i18n.T("stop.title"), i18n.F("stop.text", s.Title()), i18n.T("cancel"), i18n.T("stop")) {
		case 1:
			a.m.Stop(s, "user")
			r.toStop = nil
		case 0:
			r.toStop = nil
		}
	}
	if r.confirmRel && r.release != nil {
		switch ui.AlertDialog(c, &r.confirmRel, i18n.T("stop.title"), i18n.F("stop.text", r.release.Runtime.Spec.String()), i18n.T("cancel"), i18n.T("stop")) {
		case 1:
			a.m.Release(*r.release)
			r.release = nil
		case 0:
			r.release = nil
		}
	}
}

func (a *App) sessionCard(c *ui.Context, s *core.Session) {
	r := &a.runtimes
	t := c.Theme()
	acct := a.m.Account(s.Account)
	card(c, func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, s.Title()).FontSize(16).Bold().Grow(1).Shrink(1).SingleLine().Ellipsis("…")
			phaseBadge(c, s)
		})
		meta := []string{s.Spec.String()}
		if s.GPUName != "" {
			meta = append(meta, fmt.Sprintf("%s %s", s.GPUName, gbFromMB(s.VRAM)))
		}
		meta = append(meta, duration(s.Uptime()), s.Account)
		ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(t.TextMuted)

		if s.Busy() {
			a.progress(c, s)
		}
		if s.Err != "" {
			ui.Text(c, s.Err).TextColor(t.Danger).FontSize(13).Selectable()
		}

		if s.Phase == core.PhaseRunning {
			a.endpoints(c, s)
			a.resources(c, s)
			a.costs(c, s, acct)
		}

		ui.Row(c).Gap(8).Wrap().Children(func() {
			if s.Phase == core.PhaseRunning && s.OpenAI() != "" {
				if labeledButton(c, iconChat, i18n.T("nav.chat"), true).Clicked() {
					a.chat.session = s.ID
					a.page = pageChat
				}
			}
			if labeledButton(c, iconLog, i18n.T("runtimes.log"), false).Clicked() {
				r.showLog[s.ID] = !r.showLog[s.ID]
			}
			if s.Phase == core.PhaseRunning {
				if labeledButton(c, iconRefresh, i18n.T("runtimes.remote_log"), false).Clicked() {
					id := s.ID
					a.m.Logs(s, func(out string, err error) {
						if err != nil {
							out = err.Error()
						}
						r.remoteLog[id] = out
						r.showLog[id] = true
					})
				}
			}
			ui.Spacer(c)
			if s.Phase != core.PhaseStopping {
				if labeledButton(c, iconStop, i18n.T("stop"), false).TextColor(t.Danger).Clicked() {
					r.toStop, r.confirm = s, true
				}
			}
		})
		if r.showLog[s.ID] {
			if remote := r.remoteLog[s.ID]; remote != "" {
				ui.Text(c, i18n.T("runtimes.remote_log")).FontSize(12).Bold()
				codeBox(c, remote)
			}
			logBox(c, s)
		}
	})
}

// progress shows where a session is on its way up, and its last lines.
func (a *App) progress(c *ui.Context, s *core.Session) {
	t := c.Theme()
	steps := []string{core.PhaseValidating, core.PhaseAssigning, core.PhaseConnecting, core.PhaseGuarding, core.PhaseDeploying, core.PhaseForwarding}
	at := 0
	for i, p := range steps {
		if p == s.Phase || ((s.Phase == core.PhaseRetrying || s.Phase == core.PhaseWaiting) && p == core.PhaseAssigning) {
			at = i
		}
	}
	ui.Progress(c, float64(at+1)/float64(len(steps)+1))
	label := phaseText(s)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Spinner(c)
		ui.Text(c, label).FontSize(13)
	})
	if s.Grabbing || s.Attempt > 1 {
		ui.Text(c, i18n.F("grab.status", s.Round, s.Attempt, duration(time.Since(s.Created)))).FontSize(12).TextColor(t.TextMuted)
		if s.TryErr != "" {
			ui.Text(c, i18n.F("grab.last", s.TryErr)).FontSize(12).TextColor(t.Warning).SingleLine().Ellipsis("…")
		}
		// The countdown and the time waited move every second.
		c.After(time.Second)
	}
	n := len(s.Log)
	from := max(0, n-4)
	ui.Column(c).Gap(2).Children(func() {
		for _, l := range s.Log[from:] {
			ui.Text(c, l.Text).Font("monospace").FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		}
	})
}

func (a *App) endpoints(c *ui.Context, s *core.Session) {
	t := c.Theme()
	if len(s.Forwards) == 0 {
		return
	}
	ui.Column(c).Gap(6).Padding(12).Radius(8).Background(t.Background).Children(func() {
		for _, f := range s.Forwards {
			if f.Err != "" {
				ui.Text(c, fmt.Sprintf("%s :%d — %s", f.Port.Name, f.Port.Remote, f.Err)).TextColor(t.Danger).FontSize(13)
				continue
			}
			switch f.Port.Kind {
			case plugins.PortOpenAI:
				a.copyable(c, i18n.T("ep.openai"), s.OpenAI())
				if s.Result != nil && s.Result.Model != "" {
					a.copyable(c, i18n.T("ep.model"), s.Result.Model)
				}
				if s.Result != nil && s.Result.APIKey != "" {
					a.copyable(c, i18n.T("ep.key"), s.Result.APIKey)
				}
				ui.Row(c).Gap(8).Children(func() {
					if labeledButton(c, iconGlobe, i18n.T("ep.webchat"), false).Clicked() {
						openURL(f.URL())
					}
				})
			case plugins.PortWeb:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					a.copyable(c, f.Port.Name, f.URL())
				})
				if labeledButton(c, iconExternal, i18n.T("open"), false).Clicked() {
					openURL(f.URL())
				}
			default:
				a.copyable(c, f.Port.Name, f.Address())
			}
		}
		if s.Result != nil {
			var parts []string
			for _, kv := range s.Result.Info {
				parts = append(parts, i18n.T("info."+kv[0])+" "+kv[1])
			}
			if len(parts) > 0 {
				ui.Text(c, strings.Join(parts, " · ")).FontSize(12).TextColor(t.TextMuted)
			}
		}
	})
}

func (a *App) resources(c *ui.Context, s *core.Session) {
	st := s.Status
	if st == nil {
		return
	}
	t := c.Theme()
	ui.Grid(c).Columns(2).GapX(16).GapY(8).Children(func() {
		for _, g := range st.GPUs {
			meter(c, fmt.Sprintf("%s · %d%%", g.Name, g.Utilization), float64(g.MemoryUsed), float64(g.MemoryTotal),
				fmt.Sprintf("%s / %s", gbFromMB(g.MemoryUsed), gbFromMB(g.MemoryTotal)))
		}
		used := st.Memory.Total - st.Memory.Available
		meter(c, i18n.T("res.ram"), float64(used), float64(st.Memory.Total), fmt.Sprintf("%s / %s", gbFromMB(used>>20), gbFromMB(st.Memory.Total>>20)))
		dused := st.Disk.Total - st.Disk.Free
		meter(c, i18n.T("res.disk"), float64(dused), float64(st.Disk.Total), fmt.Sprintf("%s / %s", gbFromMB(dused>>20), gbFromMB(st.Disk.Total>>20)))
	})
	if st.Watchdog.LastActivity > 0 && s.Guards.IdleStop > 0 {
		idle := time.Since(time.Unix(int64(st.Watchdog.LastActivity), 0))
		left := time.Duration(s.Guards.IdleStop*float64(time.Minute)) - idle
		ui.Text(c, i18n.F("res.idle", duration(idle), duration(max(left, 0)))).FontSize(12).TextColor(t.TextMuted)
	}
}

func meter(c *ui.Context, label string, value, total float64, text string) {
	t := c.Theme()
	ui.Column(c.Key(label)).Gap(4).Children(func() {
		ui.Row(c).Children(func() {
			ui.Text(c, label).FontSize(12).Grow(1).SingleLine().Ellipsis("…")
			ui.Text(c, text).FontSize(12).TextColor(t.TextMuted)
		})
		if total <= 0 {
			total = 1
		}
		ui.Meter(c, value, 0, total, &ui.MeterLevels{Warning: total * 0.8, Critical: total * 0.95, NormalColor: accent})
	})
}

func (a *App) costs(c *ui.Context, s *core.Session, acct *core.Account) {
	r := &a.runtimes
	t := c.Theme()
	ui.Row(c).Gap(12).Wrap().Children(func() {
		if acct != nil && acct.Quota != nil {
			q := acct.Quota
			if s.StartBalance > 0 {
				stat(c, fmt.Sprintf("%.2f", max(0, s.StartBalance-q.Balance)), i18n.T("cost.spent"))
			}
			stat(c, fmt.Sprintf("%.2f/h", q.RateHourly), i18n.T("cost.rate"))
			stat(c, fmt.Sprintf("%.2f", q.Balance), i18n.T("cost.balance"))
		}
		if at := s.StopAt(); !at.IsZero() {
			stat(c, at.Format("15:04"), i18n.F("cost.stop_at", duration(time.Until(at))))
		}
	})
	g := r.editGuards[s.ID]
	if g == nil {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, guardsSummary(s.Guards)).FontSize(12).TextColor(t.TextMuted).Grow(1)
			if ui.Button(c, i18n.T("guard.edit")).Clicked() {
				cp := s.Guards
				r.editGuards[s.ID] = &cp
			}
		})
		return
	}
	a.guardsForm(c, g, acct)
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, i18n.T("save")).Clicked() {
			a.m.UpdateGuards(s, *g)
			delete(r.editGuards, s.ID)
		}
		if ui.Button(c, i18n.T("cancel")).Clicked() {
			delete(r.editGuards, s.ID)
		}
	})
}

func guardsSummary(g core.Guards) string {
	var parts []string
	if g.StopAfter > 0 {
		parts = append(parts, i18n.F("guard.s_after", duration(time.Duration(g.StopAfter*float64(time.Minute)))))
	}
	if g.IdleStop > 0 {
		parts = append(parts, i18n.F("guard.s_idle", g.IdleStop))
	}
	if g.SpendCap > 0 {
		parts = append(parts, i18n.F("guard.s_cap", g.SpendCap))
	}
	if g.BalanceFloor > 0 {
		parts = append(parts, i18n.F("guard.s_floor", g.BalanceFloor))
	}
	if len(parts) == 0 {
		return i18n.T("guard.none")
	}
	return i18n.T("guard.title") + ": " + strings.Join(parts, " · ")
}

func phaseBadge(c *ui.Context, s *core.Session) {
	t := c.Theme()
	color := t.TextMuted
	switch s.Phase {
	case core.PhaseRunning:
		color = t.Success
	case core.PhaseFailed:
		color = t.Danger
	case core.PhaseStopped:
		color = t.TextMuted
	default:
		color = accent
	}
	ui.Text(c, phaseText(s)).FontSize(12).Bold().TextColor(color).Padding(2, 8).Radius(999).Border(1, color).SingleLine()
}

func phaseText(s *core.Session) string {
	if s.Phase == core.PhaseDeploying && s.PluginPhase != "" {
		return i18n.T("phase.deploying") + " · " + i18n.T("pphase."+s.PluginPhase)
	}
	if s.Phase == core.PhaseWaiting {
		return i18n.F("phase.waiting_n", duration(time.Until(s.NextTry)))
	}
	if s.Phase == core.PhaseAssigning && s.Attempt > 1 {
		return i18n.F("phase.assigning_n", s.Attempt)
	}
	return i18n.T("phase." + s.Phase)
}

func stopReason(s *core.Session) string {
	if s.StopReason == "" {
		return i18n.T("phase." + s.Phase)
	}
	return i18n.T("reason." + s.StopReason)
}

func logBox(c *ui.Context, s *core.Session) {
	var b strings.Builder
	for _, l := range s.Log {
		b.WriteString(l.Time.Format("15:04:05 "))
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	codeBox(c, b.String())
}

func codeBox(c *ui.Context, text string) {
	t := c.Theme()
	ui.Scroll(c).MaxHeight(320).Radius(8).Background(t.Background).Border(1, t.Border).Children(func() {
		ui.Text(c, strings.TrimRight(text, "\n")).Font("monospace").FontSize(12).Selectable().Padding(10).PreserveSpaces()
	})
}
