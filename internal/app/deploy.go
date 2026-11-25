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
	pageScroll(c, i18n.T("nav.deploy"), nil, func() {
		acct := a.m.Active()
		if acct == nil {
			a.noAccountCard(c)
			return
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
						}
					}
				}
			})
			p = plugins.Get(d.form.Plugin)
			ui.Text(c, p.Description().In(i18n.Lang())).TextColor(t.TextMuted).FontSize(13)
			a.pluginFields(c, p)
		})

		section(c, i18n.T("deploy.hardware"), func() { a.hardware(c, acct) })
		section(c, i18n.T("deploy.grab"), func() { a.grabForm(c) })
		section(c, i18n.T("deploy.guards"), func() { a.guardsForm(c, &d.form.Guards, acct) })

		ui.Row(c).Gap(12).AlignItems(ui.Center).Wrap().Children(func() {
			if labeledButton(c, iconRocket, i18n.T("deploy.launch"), true).Disabled(acct.SignedOut).Clicked() {
				a.launch()
			}
			ui.Text(c, i18n.F("deploy.on_account", acct.Email)).TextColor(t.TextMuted).FontSize(13)
		})
		if d.err != "" {
			ui.Text(c, d.err).TextColor(t.Danger).Selectable()
		}
	})
}

func (a *App) launch() {
	d := &a.deploy
	d.err = ""
	d.form.Variant = variants[d.variant]
	p := plugins.Get(d.form.Plugin)
	v := d.values(p)
	for _, f := range p.Fields() {
		if f.Required && strings.TrimSpace(v[f.Key]) == "" {
			d.err = i18n.F("deploy.required", f.Label.In(i18n.Lang()))
			return
		}
	}
	a.m.Settings.Deploy = d.form
	a.m.SaveSettings()
	s, err := a.m.Launch(d.form)
	if err != nil {
		d.err = err.Error()
		return
	}
	a.runtimes.selected = s.ID
	a.page = pageRuntimes
}

// pluginFields builds the form of a plugin's fields.
func (a *App) pluginFields(c *ui.Context, p plugins.Plugin) {
	d := &a.deploy
	t := c.Theme()
	v := d.values(p)
	lang := i18n.Lang()
	var advanced []plugins.Field
	for _, f := range p.Fields() {
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
	_ = t
}

func (a *App) pluginField(c *ui.Context, p plugins.Plugin, f plugins.Field, v map[string]string, lang string) {
	d := &a.deploy
	t := c.Theme()
	{
		key := p.ID() + "." + f.Key
		ui.Column(c.Key(key)).Gap(4).Children(func() {
			ui.Field(c, f.Label.In(lang), func() {
				switch f.Kind {
				case plugins.FieldPassword:
					val := v[f.Key]
					if ui.TextInput(c, &val).Password().Placeholder(f.Placeholder).Changed() {
						v[f.Key] = val
					}
				case plugins.FieldNumber:
					n := d.nums[key]
					if n == nil {
						x, _ := strconv.ParseFloat(v[f.Key], 64)
						n = &x
						d.nums[key] = n
					}
					if ui.NumberInput(c, n, 0, 1e7, 1).Changed() {
						v[f.Key] = strconv.FormatFloat(*n, 'f', -1, 64)
					}
				case plugins.FieldSelect:
					val := v[f.Key]
					if val == "" && len(f.Options) > 0 {
						val = f.Options[0]
					}
					if ui.Select(c, &val, f.Options).Changed() {
						v[f.Key] = val
					}
				case plugins.FieldSwitch:
					on := v[f.Key] == "true"
					if ui.Switch(c, &on).Changed() {
						v[f.Key] = strconv.FormatBool(on)
					}
				default:
					val := v[f.Key]
					in := ui.TextInput(c, &val).Placeholder(f.Placeholder).Autocorrect(false).Capitalize(ui.CapitalizeNone)
					if in.Changed() {
						v[f.Key] = val
					}
				}
			})
			if h := f.Help.In(lang); h != "" {
				ui.Text(c, h).FontSize(12).TextColor(t.TextMuted)
			}
		})
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
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Switch(c, &d.form.HighMem).Label(i18n.T("hw.highmem"))
		ui.Text(c, i18n.T("hw.highmem"))
	})
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
		if d.form.RetrySeconds < 10 {
			d.form.RetrySeconds = 30
		}
		ui.Grid(c).Columns(2).GapX(16).GapY(12).Children(func() {
			ui.Field(c, i18n.T("grab.interval"), func() { ui.NumberInput(c, &d.form.RetrySeconds, 10, 3600, 5) })
			ui.Field(c, i18n.T("grab.max_wait"), func() { ui.NumberInput(c, &d.form.MaxWait, 0, 10080, 10) })
		})
	}
	if len(a.m.Accounts) > 1 {
		switchRow(c, &d.form.AnyAccount, i18n.T("grab.any_account"))
		ui.Text(c, i18n.T("grab.any_account_help")).FontSize(12).TextColor(t.TextMuted)
	}
}

// switchRow is a switch with its label after it.
func switchRow(c *ui.Context, on *bool, label string) {
	ui.Row(c.Key(label)).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Switch(c, on).Label(label)
		ui.Text(c, label).Grow(1).Shrink(1)
	})
}

// guardsForm edits the limits on what a runtime costs.
func (a *App) guardsForm(c *ui.Context, g *core.Guards, acct *core.Account) {
	t := c.Theme()
	ui.Grid(c).Columns(2).GapX(16).GapY(12).Children(func() {
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

func (a *App) noAccountCard(c *ui.Context) {
	t := c.Theme()
	card(c, func() {
		ui.Text(c, i18n.T("account.welcome")).FontSize(17).Bold()
		ui.Text(c, i18n.T("account.welcome_text")).TextColor(t.TextMuted)
		if labeledButton(c, iconPlus, i18n.T("account.add"), true).Clicked() {
			a.page = pageAccounts
			a.startLogin()
		}
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
