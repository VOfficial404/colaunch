package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/colab"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

type deployState struct {
	form     core.DeployForm
	nums     map[string]*float64
	variant  int
	err      string
	advanced bool
	// fieldErr is why a field's value won't do, by plugin and field key.
	fieldErr map[string]string
	// grabOpen and guardsOpen unfold the sections that show as a line.
	grabOpen   bool
	guardsOpen bool
	// launched is the session the form last started, whose error shows
	// under the form when llama.cpp refused its model.
	launched string
	// freeConfirm asks before a web task launches on the free tier.
	freeConfirm bool
}

var variants = []string{"cpu", "gpu", "tpu"}

// Accelerators Colab has had, for when the account hasn't said yet.
var (
	knownGPUs = []string{"T4", "L4", "A100", "H100", "G4"}
	knownTPUs = []string{"V5E1", "V6E1", "V2-8"}
)

func (d *deployState) init(m *core.Manager) {
	d.form = m.Settings.Deploy
	if d.form.Values == nil {
		d.form.Values = map[string]map[string]string{}
	}
	if plugins.Get(d.form.Plugin) == nil {
		d.form.Plugin = "llama"
	}
	d.nums = map[string]*float64{}
	d.fieldErr = map[string]string{}
	for i, v := range variants {
		if v == d.form.Variant {
			d.variant = i
		}
	}
}

func (d *deployState) values(p plugins.Plugin) map[string]string {
	v := d.form.Values[p.ID()]
	if v == nil {
		v = plugins.Defaults(p)
		d.form.Values[p.ID()] = v
	}
	for _, f := range p.Fields() {
		if _, ok := v[f.Key]; !ok {
			v[f.Key] = f.Default
		}
	}
	return v
}

func (a *App) deployPage(c *ui.Context) {
	d := &a.deploy
	t := c.Theme()
	acct := a.m.Active()
	if acct == nil {
		pageScroll(c, i18n.T("nav.deploy"), nil, func() { a.onboarding(c) })
		return
	}
	pageFrame(c, i18n.T("nav.deploy"), nil, nil, func() {
		if isFreeTier(acct) {
			freeTierNotice(c)
		}
		if d.form.Plugin == comfyPlugin {
			comfyNotice(c)
		}
		// What and where.
		p := plugins.Get(d.form.Plugin)
		section(c, i18n.T("deploy.what"), func() {
			all := plugins.All()
			names := make([]string, len(all))
			chosen := ""
			for i, pl := range all {
				names[i] = pl.Name().In(i18n.Lang())
				if pl.ID() == d.form.Plugin {
					chosen = names[i]
				}
			}
			ui.Field(c, i18n.T("deploy.plugin"), func() {
				if ui.Select(c, &chosen, names).Changed() {
					for i, n := range names {
						if n == chosen {
							d.form.Plugin = all[i].ID()
							d.launched = ""
						}
					}
				}
			})
			p = plugins.Get(d.form.Plugin)
			ui.Text(c, p.Description().In(i18n.Lang())).TextColor(t.TextMuted).FontSize(13)
			a.pluginFields(c, p)
		})

		section(c, i18n.T("deploy.hardware"), func() { a.hardware(c, acct) })
		foldSection(c, "grab", i18n.T("deploy.grab"), grabSummary(d.form.Machine, a.m.Settings.MinRetry()), &d.grabOpen, func() { a.grabForm(c) })
		foldSection(c, "guards", i18n.T("deploy.guards"), guardsLine(d.form.Guards), &d.guardsOpen, func() { a.guardsForm(c, &d.form.Guards, acct) })
	}, func() { a.launchBar(c, acct) })
}

// launchBar stays under the form: what is about to happen in a line, and
// the button that starts it.
func (a *App) launchBar(c *ui.Context, acct *core.Account) {
	d := &a.deploy
	t := c.Theme()
	d.form.Variant = variants[d.variant]
	summary := a.launchSummary(acct)
	noCap := d.form.SpendCap <= 0
	blocked, risky := a.freeWeb(acct)
	button := func() {
		b := labeledButton(c, iconRocket, i18n.T("deploy.launch"), true).Disabled(acct.SignedOut || blocked)
		if b.Clicked() {
			if risky {
				d.freeConfirm = true
			} else {
				a.launch()
			}
		}
	}
	capNote := func() {
		if risky {
			ui.Text(c, i18n.T("freeweb.on")).FontSize(12).Bold().TextColor(t.Danger).SingleLine()
		}
		if noCap {
			ui.Text(c, i18n.T("bar.no_cap")).FontSize(12).Bold().TextColor(t.Warning).Tooltip(i18n.T("bar.no_cap_tip")).SingleLine()
		}
	}
	if blocked {
		a.freeWebNotice(c)
	}
	a.freeWebDialog(c)
	if isPhone(c) {
		ui.Text(c, summary).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			capNote()
			ui.Spacer(c)
			button()
		})
	} else {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, summary).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Tooltip(summary).Grow(1).Shrink(1)
			capNote()
			button()
		})
	}
	if d.err != "" {
		a.errorNotice(c, "deploy", d.err)
		a.comfySuggestion(c, d.err, d.values(plugins.Get(d.form.Plugin))[comfyRepo])
	} else if s := a.m.Session(d.launched); s != nil && s.Phase == core.PhaseFailed {
		// llama.cpp refuses image and video models before a runtime is
		// assigned: the way on is from here.
		if _, ok := suggestComfy(s.Err, s.Values[comfyRepo]); ok {
			a.errorNotice(c, "deploy-"+s.ID, s.Err)
			a.comfySuggestion(c, s.Err, s.Values[comfyRepo])
		}
	}
}

// launchSummary is the deployment in a line: the accelerators in the
// order asked, how it grabs, what stops it, the account and about what an
// hour costs.
func (a *App) launchSummary(acct *core.Account) string {
	d := &a.deploy
	m := d.form.Machine
	cands := m.Candidates()
	names := make([]string, len(cands))
	for i, x := range cands {
		names[i] = x
		if x == "NONE" {
			names[i] = i18n.T("hw.cpu")
		}
	}
	parts := []string{strings.Join(names, " → ")}
	if m.Grab {
		parts = append(parts, i18n.F("bar.grab_every", max(m.RetrySeconds, a.m.Settings.MinRetry())))
	} else {
		parts = append(parts, i18n.T("bar.no_grab"))
	}
	parts = append(parts, guardsLine(d.form.Guards), acct.Email)
	return strings.Join(parts, " · ")
}

// foldSection is a section that shows as its title and a line of what it
// says until opened.
func foldSection(c *ui.Context, key, title, summary string, open *bool, fn func()) {
	t := c.Theme()
	card(c.Key("fold-"+key), func() {
		head := ui.Row(c).Gap(10).AlignItems(ui.Center).Focusable().Role(ui.RoleButton).Label(title).Expanded(*open)
		head.Children(func() {
			ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
				ui.Text(c, title).Bold().FontSize(15)
				if !*open {
					ui.Text(c, summary).FontSize(12).TextColor(t.TextMuted)
				}
			})
			icon := iconChevronRight
			if *open {
				icon = iconChevronDown
			}
			ui.Icon(c, icon).TextColor(t.TextMuted)
		})
		if head.Clicked() {
			*open = !*open
		}
		if *open {
			fn()
		}
	})
}

func (a *App) launch() {
	d := &a.deploy
	d.err, d.launched = "", ""
	d.form.Variant = variants[d.variant]
	p := plugins.Get(d.form.Plugin)
	v := d.values(p)
	clear(d.fieldErr)
	for _, f := range p.Fields() {
		if f.Required && strings.TrimSpace(v[f.Key]) == "" {
			d.fieldErr[p.ID()+"."+f.Key] = i18n.T("form.required")
			if d.err == "" {
				d.err = i18n.F("deploy.required", f.Label.In(i18n.Lang()))
			}
			if f.Advanced {
				d.advanced = true
			}
		}
	}
	if d.err != "" {
		return
	}
	a.m.Settings.Deploy = d.form
	a.m.SaveSettings()
	s, err := a.m.Launch(d.form)
	if err != nil {
		d.err = err.Error()
		return
	}
	a.runtimes.remember(s.ID, d.form.Machine)
	a.runtimes.selected = s.ID
	d.launched = s.ID
	a.page = pageRuntimes
}

// pluginFields builds the form of a plugin's fields.
func (a *App) pluginFields(c *ui.Context, p plugins.Plugin) {
	d := &a.deploy
	t := c.Theme()
	v := d.values(p)
	lang := i18n.Lang()
	var advanced []plugins.Field
	required := false
	for _, f := range p.Fields() {
		required = required || f.Required
		if f.Advanced {
			advanced = append(advanced, f)
			continue
		}
		a.pluginField(c, p, f, v, lang)
	}
	if len(advanced) > 0 {
		ui.Collapsible(c, i18n.T("deploy.advanced"), &d.advanced, func() {
			ui.Column(c).Gap(12).PaddingY(4).Children(func() {
				for _, f := range advanced {
					a.pluginField(c, p, f, v, lang)
				}
			})
		})
	}
	if required {
		ui.Text(c, i18n.T("form.required_note")).FontSize(12).TextColor(t.TextMuted)
	}
}

// fieldLabel is a field's label, marked when it must be filled in.
func fieldLabel(label string, required bool) string {
	if required {
		return label + " *"
	}
	return label
}

// example is a placeholder as an example of what to type.
func example(s string) string {
	if s == "" {
		return ""
	}
	return i18n.F("form.example", s)
}

func (a *App) pluginField(c *ui.Context, p plugins.Plugin, f plugins.Field, v map[string]string, lang string) {
	d := &a.deploy
	t := c.Theme()
	key := p.ID() + "." + f.Key
	changed := false
	ui.Column(c.Key(key)).Gap(4).Children(func() {
		ui.Field(c, fieldLabel(f.Label.In(lang), f.Required), func() {
			switch f.Kind {
			case plugins.FieldPassword:
				val := v[f.Key]
				if ui.TextInput(c, &val).Password().Placeholder(example(f.Placeholder)).Changed() {
					v[f.Key], changed = val, true
				}
			case plugins.FieldNumber:
				n := d.nums[key]
				if n == nil {
					x, _ := strconv.ParseFloat(v[f.Key], 64)
					n = &x
					d.nums[key] = n
				}
				if ui.NumberInput(c, n, 0, 1e7, 1).Changed() {
					v[f.Key], changed = strconv.FormatFloat(*n, 'f', -1, 64), true
				}
			case plugins.FieldSelect:
				val := v[f.Key]
				if val == "" && len(f.Options) > 0 {
					val = f.Options[0]
				}
				if ui.Select(c, &val, f.Options).Changed() {
					v[f.Key], changed = val, true
				}
			case plugins.FieldSwitch:
				on := v[f.Key] == "true"
				if ui.Switch(c, &on).Changed() {
					v[f.Key], changed = strconv.FormatBool(on), true
				}
			default:
				val := v[f.Key]
				in := ui.TextInput(c, &val).Placeholder(example(f.Placeholder)).Autocorrect(false).Capitalize(ui.CapitalizeNone)
				if in.Changed() {
					v[f.Key], changed = val, true
				}
			}
		}).Error(d.fieldErr[key])
		if h := f.Help.In(lang); h != "" {
			ui.Text(c, h).FontSize(12).TextColor(t.TextMuted)
		}
	})
	if changed && d.fieldErr[key] != "" {
		delete(d.fieldErr, key)
		d.err = ""
	}
}

func (a *App) hardware(c *ui.Context, acct *core.Account) {
	d := &a.deploy
	t := c.Theme()
	ui.Segmented(c, &d.variant, i18n.T("hw.cpu"), i18n.T("hw.gpu"), i18n.T("hw.tpu")).Label(i18n.T("deploy.hardware"))
	variant := variants[d.variant]
	if variant != "cpu" {
		cv := colab.GPU
		known := knownGPUs
		if variant == "tpu" {
			cv, known = colab.TPU, knownTPUs
		}
		eligible, ineligible := acct.Accelerators(cv)
		options := eligible
		if len(options) == 0 && len(ineligible) == 0 {
			options = known
		}
		if len(options) > 0 && !contains(options, d.form.Accelerator) {
			d.form.Accelerator = options[0]
		}
		ui.Field(c, i18n.T("hw.accelerator"), func() {
			if len(options) == 0 {
				ui.Text(c, i18n.T("hw.none_eligible")).TextColor(t.Warning)
				return
			}
			ui.Select(c, &d.form.Accelerator, options)
		})
		if len(ineligible) > 0 {
			ui.Text(c, i18n.F("hw.ineligible", strings.Join(ineligible, ", "))).FontSize(12).TextColor(t.TextMuted)
		}
		a.fallbacks(c, options)
	}
	switchRow(c, &d.form.HighMem, i18n.T("hw.highmem"))
	if variant == "gpu" {
		ui.Field(c, i18n.T("hw.min_vram"), func() {
			ui.NumberInput(c, &d.form.MinVRAM, 0, 192, 1).Width(160)
		})
		ui.Text(c, i18n.T("hw.min_vram_help")).FontSize(12).TextColor(t.TextMuted)
	}
}

// fallbacks chooses the accelerators to ask for when the chosen one isn't
// available, among those of the variant.
func (a *App) fallbacks(c *ui.Context, options []string) {
	d := &a.deploy
	t := c.Theme()
	var others, kept []string
	for _, o := range options {
		if o != d.form.Accelerator {
			others = append(others, o)
		}
	}
	// They are tried in the order they show.
	for _, o := range others {
		if contains(d.form.Fallbacks, o) {
			kept = append(kept, o)
		}
	}
	d.form.Fallbacks = kept
	if len(others) == 0 {
		return
	}
	ui.Text(c, i18n.T("hw.fallbacks")).FontSize(13)
	ui.Row(c).Gap(16).Wrap().Children(func() {
		for _, o := range others {
			on := contains(d.form.Fallbacks, o)
			if ui.Checkbox(c.Key("fallback-"+o), &on, o).Changed() {
				if on {
					d.form.Fallbacks = append(d.form.Fallbacks, o)
				} else {
					d.form.Fallbacks = slices.DeleteFunc(d.form.Fallbacks, func(x string) bool { return x == o })
				}
			}
		}
	})
	ui.Text(c, i18n.T("hw.fallbacks_help")).FontSize(12).TextColor(t.TextMuted)
}

// grabForm edits how hard to try for a runtime.
func (a *App) grabForm(c *ui.Context) {
	d := &a.deploy
	t := c.Theme()
	switchRow(c, &d.form.Grab, i18n.T("grab.on"))
	ui.Text(c, i18n.T("grab.help")).FontSize(12).TextColor(t.TextMuted)
	if d.form.Grab {
		least := a.m.Settings.MinRetry()
		if d.form.RetrySeconds < least {
			d.form.RetrySeconds = 30
		}
		ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(12).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				ui.Field(c, i18n.T("grab.interval"), func() { ui.NumberInput(c, &d.form.RetrySeconds, least, 3600, 1) })
				if d.form.RetrySeconds < core.MinRetrySeconds {
					riskyWarning(c, i18n.T("grab.too_fast"))
				}
			})
			ui.Field(c, i18n.T("grab.max_wait"), func() { ui.NumberInput(c, &d.form.MaxWait, 0, 10080, 10) })
		})
	}
}

// grabSummary is how a machine is grabbed, in a line.
func grabSummary(m core.Machine, least float64) string {
	if !m.Grab {
		return i18n.T("grab.s_off")
	}
	line := i18n.F("grab.s_on", max(m.RetrySeconds, least))
	if m.MaxWait > 0 {
		return line + " · " + i18n.F("grab.s_max", duration(minutes(m.MaxWait)))
	}
	return line + " · " + i18n.T("grab.s_forever")
}

// switchRow is a switch with its label after it; a click anywhere on the
// row flips it. It reports whether it changed.
func switchRow(c *ui.Context, on *bool, label string) bool {
	changed := false
	row := ui.Row(c.Key(label)).Gap(10).AlignItems(ui.Center).PaddingY(4)
	row.Children(func() {
		changed = ui.Switch(c, on).Label(label).Changed()
		ui.Text(c, label).Grow(1).Shrink(1)
	})
	if !changed && row.Clicked() {
		*on = !*on
		changed = true
	}
	return changed
}

// guardsForm edits the limits on what a runtime costs.
func (a *App) guardsForm(c *ui.Context, g *core.Guards, acct *core.Account) {
	t := c.Theme()
	ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(12).Children(func() {
		ui.Field(c, i18n.T("guard.stop_after"), func() { ui.NumberInput(c, &g.StopAfter, 0, 100000, 15) })
		ui.Field(c, i18n.T("guard.idle"), func() { ui.NumberInput(c, &g.IdleStop, 0, 100000, 5) })
		ui.Field(c, i18n.T("guard.cap"), func() { ui.NumberInput(c, &g.SpendCap, 0, 100000, 1) })
		ui.Field(c, i18n.T("guard.floor"), func() { ui.NumberInput(c, &g.BalanceFloor, 0, 100000, 1) })
	})
	ui.Text(c, i18n.T("guard.help")).FontSize(12).TextColor(t.TextMuted)
	if acct != nil && acct.Quota != nil {
		ui.Text(c, balanceLine(acct)).FontSize(13).TextColor(t.TextMuted)
	}
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}
