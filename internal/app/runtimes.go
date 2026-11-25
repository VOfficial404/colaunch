package app

import (
	"fmt"
	"sort"
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
	// machines are what the sessions launched from here asked for, to
	// show the queue of accelerators while grabbing.
	machines map[string]core.Machine
	// logs are the sessions' logs as text, kept while they don't change.
	logs map[string]*logText
	// hidden are ended sessions removed from the history, until the
	// toast that can undo it closes.
	hidden map[string]bool
}

// logText is a session's log as one text, for the lines it had.
type logText struct {
	n    int
	last core.LogLine
	text string
}

func (r *runtimesState) init() {
	if r.showLog == nil {
		r.showLog, r.remoteLog, r.editGuards = map[string]bool{}, map[string]string{}, map[string]*core.Guards{}
	}
	if r.machines == nil {
		r.machines, r.logs, r.hidden = map[string]core.Machine{}, map[string]*logText{}, map[string]bool{}
	}
}

// remember keeps what a session launched from here asked for.
func (r *runtimesState) remember(id string, m core.Machine) {
	r.init()
	r.machines[id] = m
}

// grabWaiting reports whether a session is still trying for a runtime,
// round after round, without one yet.
func grabWaiting(s *core.Session) bool {
	return s.Grabbing && (s.Phase == core.PhaseWaiting || s.Phase == core.PhaseAssigning)
}

// liveRank orders the runtimes: running, then starting, then waiting for
// one, then the others.
func liveRank(s *core.Session) int {
	switch {
	case s.Phase == core.PhaseRunning:
		return 0
	case grabWaiting(s):
		return 2
	case s.Busy():
		return 1
	}
	return 3
}

func sortLive(live []*core.Session) {
	sort.SliceStable(live, func(i, j int) bool { return liveRank(live[i]) < liveRank(live[j]) })
}

// shortTitle is a session's title without the owner of its repository.
func shortTitle(s *core.Session) string {
	title := s.Title()
	if i := strings.LastIndexByte(title, '/'); i >= 0 && i < len(title)-1 {
		return title[i+1:]
	}
	return title
}

// waitColor marks waiting for a runtime, apart from starting one.
func waitColor(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.Hex("#60a5fa")
	}
	return ui.Hex("#1d4ed8")
}

func (a *App) runtimesPage(c *ui.Context) {
	r := &a.runtimes
	r.init()
	t := c.Theme()
	pageScroll(c, i18n.T("nav.runtimes"), func() {
		if iconButton(c, iconRefresh, i18n.T("refresh")).Clicked() {
			for _, acct := range a.m.Accounts {
				a.m.Refresh(acct)
			}
		}
	}, func() {
		live := a.m.Live()
		sortLive(live)
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
					ui.Row(c.Key(u.Runtime.Name)).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
						ui.Text(c, u.Runtime.Spec.String()).Bold()
						ui.Text(c, u.Account.Email).TextColor(t.TextMuted).FontSize(12).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
						if labeledButton(c, iconStop, i18n.T("stop"), false).TextColor(t.Danger).Clicked() {
							rel := u
							r.release, r.confirmRel = &rel, true
						}
					})
				}
			})
		}
		var ended []*core.Session
		for _, s := range a.m.Sessions {
			if s.Ended() && !r.hidden[s.ID] {
				ended = append(ended, s)
			}
		}
		if len(ended) > 0 {
			section(c, i18n.T("runtimes.history"), func() {
				for i := len(ended) - 1; i >= 0; i-- {
					s := ended[i]
					ui.Column(c.Key("h" + s.ID)).Gap(4).Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, shortTitle(s)).Bold().Grow(1).Shrink(1).SingleLine().Ellipsis("…")
							phaseBadge(c, s)
							if iconButton(c, iconLog, i18n.T("runtimes.log")).Clicked() {
								r.showLog[s.ID] = !r.showLog[s.ID]
							}
							if iconButton(c, iconTrash, i18n.T("remove")).Clicked() {
								id := s.ID
								a.undoable(i18n.T("history.removed"),
									func() { r.hidden[id] = true },
									func() { delete(r.hidden, id) },
									func() {
										delete(r.hidden, id)
										if s := a.m.Session(id); s != nil && s.Ended() {
											a.m.Forget(s)
										}
									})
							}
						})
						ui.Text(c, fmt.Sprintf("%s · %s · %s", s.Spec, duration(s.Uptime()), stopReason(s))).FontSize(12).TextColor(t.TextMuted)
						a.errorNotice(c, "h"+s.ID, s.Err)
						if r.showLog[s.ID] {
							a.logBox(c, s)
						}
					})
				}
			})
		}
	})
	if r.confirm && r.toStop != nil {
		s := r.toStop
		title, text, confirm := i18n.T("stop.run_title"), i18n.F("stop.run_text", shortTitle(s)), i18n.T("stop.run_confirm")
		if s.Grabbing && (grabWaiting(s) || s.Phase == core.PhaseValidating) {
			title, text, confirm = i18n.T("stop.grab_title"), i18n.F("stop.grab_text", shortTitle(s)), i18n.T("stop.grab_confirm")
		}
		switch dangerConfirm(c, &r.confirm, title, text, i18n.T("cancel"), confirm) {
		case 1:
			a.m.Stop(s, "user")
			r.toStop = nil
		case 0:
			r.toStop = nil
		}
	}
	if r.confirmRel && r.release != nil {
		switch dangerConfirm(c, &r.confirmRel, i18n.T("stop.run_title"), i18n.F("stop.run_text", r.release.Runtime.Spec.String()), i18n.T("cancel"), i18n.T("stop.run_confirm")) {
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
	waiting := grabWaiting(s)
	el := card(c, func() {
		if isPhone(c) {
			// The badge on a line of its own leaves the title its width.
			ui.Text(c, shortTitle(s)).FontSize(16).Bold().SingleLine().Ellipsis("…")
			ui.Row(c).Children(func() { phaseBadge(c, s) })
		} else {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, shortTitle(s)).FontSize(16).Bold().Grow(1).Shrink(1).SingleLine().Ellipsis("…").Tooltip(s.Title())
				phaseBadge(c, s)
			})
		}
		meta := []string{s.Spec.String()}
		if s.GPUName != "" {
			meta = append(meta, fmt.Sprintf("%s %s", s.GPUName, gbFromMB(s.VRAM)))
		}
		if !waiting {
			meta = append(meta, duration(s.Uptime()))
		}
		meta = append(meta, s.Account)
		ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(t.TextMuted)

		if waiting {
			a.waitPanel(c, s)
		} else if s.Busy() {
			a.progress(c, s)
		}
		a.errorNotice(c, s.ID, s.Err)

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
				label := i18n.T("stop")
				if waiting {
					label = i18n.T("stop.grab_confirm")
				}
				if labeledButton(c, iconStop, label, false).TextColor(t.Danger).Clicked() {
					r.toStop, r.confirm = s, true
				}
			}
		})
		if r.showLog[s.ID] {
			if remote := r.remoteLog[s.ID]; remote != "" {
				ui.Text(c, i18n.T("runtimes.remote_log")).FontSize(12).Bold()
				codeBox(c, remote)
			}
			ui.Text(c, i18n.T("runtimes.log")).FontSize(12).Bold()
			a.logBox(c, s)
		}
	})
	if waiting {
		el.Border(1, waitColor(t))
	}
}

// waitPanel shows a session waiting for a runtime: when the next round
// starts, in large figures, the accelerators it asks for, and how long it
// has waited.
func (a *App) waitPanel(c *ui.Context, s *core.Session) {
	t := c.Theme()
	color := waitColor(t)
	queue := []string{s.Spec.Accelerator}
	var maxWait float64
	if m, ok := a.runtimes.machines[s.ID]; ok {
		queue, maxWait = m.Candidates(), m.MaxWait
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconHourglass).TextColor(color)
			ui.Text(c, i18n.T("wait.title")).Bold().TextColor(color)
		})
		if s.Phase == core.PhaseWaiting {
			ui.Row(c).Gap(10).AlignItems(ui.End).Wrap().Children(func() {
				ui.Text(c, countdown(time.Until(s.NextTry))).FontSize(28).Bold().SingleLine().Label(i18n.T("wait.next"))
				ui.Column(c).PaddingY(4).Children(func() {
					ui.Text(c, i18n.T("wait.next")).FontSize(12).TextColor(t.TextMuted)
					ui.Text(c, i18n.F("wait.next_at", s.NextTry.Format("15:04:05"))).FontSize(12).TextColor(t.TextMuted)
				})
			})
		} else {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, i18n.F("wait.now", s.Spec.Accelerator)).FontSize(13)
			})
		}
		// The queue, the accelerator asked for now stands out.
		ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, i18n.T("wait.queue")).FontSize(12).TextColor(t.TextMuted)
			for i, acc := range queue {
				if i > 0 {
					ui.Text(c.Key("arrow"+acc), "→").FontSize(12).TextColor(t.TextMuted)
				}
				chip := ui.Text(c.Key("q"+acc), acc).FontSize(12).Padding(1, 8).Radius(999).Border(1, t.Border).SingleLine()
				if acc == s.Spec.Accelerator {
					chip.Bold().TextColor(color).Border(1, color)
				}
			}
		})
		waited := time.Since(s.Created)
		line := i18n.F("wait.round", s.Round, s.Attempt) + " · "
		if maxWait > 0 {
			line += i18n.F("wait.waited_max", duration(waited), duration(minutes(maxWait)))
		} else {
			line += i18n.F("wait.waited", duration(waited))
		}
		ui.Text(c, line).FontSize(12).TextColor(t.TextMuted)
		if s.TryErr != "" {
			ui.Text(c, i18n.F("grab.last", shortError(s.TryErr))).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Tooltip(s.TryErr)
		}
	})
	// The countdown and the time waited move every second.
	c.After(time.Second)
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
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Spinner(c)
		ui.Text(c, phaseText(s)).FontSize(13)
	})
	if s.Attempt > 1 {
		ui.Text(c, i18n.F("grab.status", s.Round, s.Attempt, duration(time.Since(s.Created)))).FontSize(12).TextColor(t.TextMuted)
		if s.TryErr != "" {
			ui.Text(c, i18n.F("grab.last", shortError(s.TryErr))).FontSize(12).TextColor(t.Warning).SingleLine().Ellipsis("…")
		}
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
	ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(8).Children(func() {
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
		left := minutes(s.Guards.IdleStop) - idle
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
			stat(c, fmt.Sprintf("%.2f", q.RateHourly), i18n.T("cost.rate"))
			stat(c, fmt.Sprintf("%.2f", q.Balance), i18n.T("cost.balance"))
		}
		if at := s.StopAt(); !at.IsZero() {
			stat(c, duration(time.Until(at)), i18n.F("cost.stop_at", at.Format("15:04")))
		}
	})
	g := r.editGuards[s.ID]
	if g == nil {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, guardsSummary(s.Guards)).FontSize(12).TextColor(t.TextMuted).Grow(1).Shrink(1)
			if ui.Button(c, i18n.T("guard.edit")).Clicked() {
				cp := s.Guards
				r.editGuards[s.ID] = &cp
			}
		})
		return
	}
	a.guardsForm(c, g, acct)
	ui.Row(c).Gap(8).Children(func() {
		ui.Spacer(c)
		if ui.Button(c, i18n.T("cancel")).Clicked() {
			delete(r.editGuards, s.ID)
		}
		if ui.PrimaryButton(c, i18n.T("save")).Clicked() {
			a.m.UpdateGuards(s, *g)
			delete(r.editGuards, s.ID)
		}
	})
}

// guardsLine is what stops a runtime, in a line.
func guardsLine(g core.Guards) string {
	var parts []string
	if g.StopAfter > 0 {
		parts = append(parts, i18n.F("guard.s_after", duration(minutes(g.StopAfter))))
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
		return i18n.T("guard.none_short")
	}
	return strings.Join(parts, " · ")
}

func guardsSummary(g core.Guards) string {
	if !g.Any() {
		return i18n.T("guard.none")
	}
	return i18n.F("guard.summary", guardsLine(g))
}

func phaseBadge(c *ui.Context, s *core.Session) {
	t := c.Theme()
	var color ui.Color
	switch {
	case s.Phase == core.PhaseRunning:
		color = t.Success
	case s.Phase == core.PhaseFailed:
		color = t.Danger
	case s.Phase == core.PhaseStopped:
		color = t.TextMuted
	case grabWaiting(s):
		color = waitColor(t)
	default:
		color = t.Accent
	}
	ui.Text(c, phaseShort(s)).FontSize(12).Bold().TextColor(color).Padding(2, 8).Radius(999).Border(1, color).SingleLine()
}

// phaseShort is a session's phase in a word or two, for its badge.
func phaseShort(s *core.Session) string {
	switch {
	case grabWaiting(s):
		return i18n.T("badge.grabbing")
	case s.Phase == core.PhaseRetrying:
		return i18n.T("phase.assigning")
	}
	return i18n.T("phase." + s.Phase)
}

func phaseText(s *core.Session) string {
	if s.Phase == core.PhaseDeploying && s.PluginPhase != "" {
		return i18n.T("phase.deploying") + " · " + i18n.T("pphase."+s.PluginPhase)
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

// logBox shows a session's log, written out again only when it has new
// lines.
func (a *App) logBox(c *ui.Context, s *core.Session) {
	r := &a.runtimes
	r.init()
	n := len(s.Log)
	lt := r.logs[s.ID]
	if lt == nil || lt.n != n || (n > 0 && lt.last != s.Log[n-1]) {
		var b strings.Builder
		for _, l := range s.Log {
			b.WriteString(l.Time.Format("15:04:05 "))
			b.WriteString(l.Text)
			b.WriteByte('\n')
		}
		lt = &logText{n: n, text: b.String()}
		if n > 0 {
			lt.last = s.Log[n-1]
		}
		r.logs[s.ID] = lt
	}
	codeBox(c, lt.text)
}

func codeBox(c *ui.Context, text string) {
	t := c.Theme()
	ui.Scroll(c).MaxHeight(320).Radius(8).Background(t.Background).Border(1, t.Border).Children(func() {
		ui.Text(c, strings.TrimRight(text, "\n")).Font("monospace").FontSize(12).Selectable().Padding(10).PreserveSpaces()
	})
}
