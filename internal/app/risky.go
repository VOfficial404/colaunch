package app

import (
	"github.com/egoist/mygo/ui"

	"colaunch/internal/core"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

const docsRisky = "https://github.com/VOfficial404/colaunch/blob/main/docs/risky.md"

// riskyState is the confirmation a risky feature waits on.
type riskyState struct {
	confirm bool
	// on is the setting the confirmation turns on, title and text what
	// it says.
	on          *bool
	title, text string
}

// riskyFeature is a switch of the settings' risky features.
type riskyFeature struct {
	on                       *bool
	label, help, title, text string
	// off runs when it's turned off.
	off func()
}

func (a *App) riskyFeatures() []riskyFeature {
	r := &a.m.Settings.Risky
	return []riskyFeature{
		{on: &r.PublicLinks, label: i18n.T("risky.public_links"), help: i18n.T("risky.public_links_help"),
			title: i18n.T("risky.public_links_title"), text: i18n.F("risky.public_links_text", colabFAQ),
			off: a.m.StopAllPublicLinks},
		{on: &r.FastGrab, label: i18n.T("risky.fast_grab"), help: i18n.T("risky.fast_grab_help"),
			title: i18n.T("risky.fast_grab_title"), text: i18n.F("risky.fast_grab_text", colabFAQ)},
		{on: &r.FreeWebUI, label: i18n.T("risky.free_web_ui"), help: i18n.T("risky.free_web_ui_help"),
			title: i18n.T("risky.free_web_ui_title"), text: i18n.F("risky.free_web_ui_text", colabFAQ)},
	}
}

// riskyWarning is a line in the theme's Danger color, with a warning sign.
func riskyWarning(c *ui.Context, text string) {
	t := c.Theme()
	ui.Row(c.Key("warn-" + text)).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, iconAlert).Size(16, 16).TextColor(t.Danger)
		ui.Text(c, text).FontSize(12).Bold().TextColor(t.Danger).Shrink(1)
	})
}

// riskySection is the last of the settings: the features that make Colab
// likelier to end runtimes or restrict the account. Turning one on asks
// first; turning it off doesn't.
func (a *App) riskySection(c *ui.Context) {
	r := &a.risky
	t := c.Theme()
	section(c, i18n.T("risky.title"), func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(10, 12).Radius(8).Border(1, t.Danger).Background(t.Danger.Alpha(0.08)).Children(func() {
			ui.Icon(c, iconAlert).TextColor(t.Danger)
			ui.Text(c, i18n.T("risky.warning")).Bold().FontSize(13).TextColor(t.Danger).Grow(1).Shrink(1)
		})
		ui.Text(c, i18n.T("risky.intro")).FontSize(12).TextColor(t.TextMuted)
		for _, f := range a.riskyFeatures() {
			on := *f.on
			ui.Column(c.Key(f.label)).Gap(2).Children(func() {
				if switchRow(c, &on, f.label) {
					if on {
						r.on, r.title, r.text, r.confirm = f.on, f.title, f.text, true
					} else {
						*f.on = false
						if f.off != nil {
							f.off()
						}
						a.m.SaveSettings()
					}
				}
				ui.Text(c, f.help).FontSize(12).TextColor(t.TextMuted)
			})
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if labeledButton(c, iconExternal, i18n.T("risky.faq"), false).Clicked() {
				openURL(colabFAQ)
			}
			if labeledButton(c, iconExternal, i18n.T("risky.docs"), false).Clicked() {
				openURL(docsRisky)
			}
		})
	}).Border(1, t.Danger)
}

// riskyDialog asks before a risky feature goes on.
func (a *App) riskyDialog(c *ui.Context) {
	r := &a.risky
	if !r.confirm || r.on == nil {
		return
	}
	switch dangerConfirm(c, &r.confirm, r.title, r.text, i18n.T("cancel"), i18n.T("risky.confirm")) {
	case 1:
		*r.on = true
		a.m.SaveSettings()
		r.on = nil
	case 0:
		r.on = nil
	}
}

// publicLink shows, under a forwarded port, its public link, or the button
// that makes one when the settings allow.
func (a *App) publicLink(c *ui.Context, s *core.Session, f *core.ForwardState) {
	t := c.Theme()
	if !core.Shareable(f.Port.Kind) {
		return
	}
	l := s.PublicLink(f.Port.Remote)
	if l == nil && !a.m.Settings.Risky.PublicLinks {
		return
	}
	ui.Column(c.Key("share")).Gap(6).Children(func() {
		switch {
		case l != nil && l.URL != "":
			ui.Column(c).Gap(6).Padding(8, 10).Radius(8).Border(1, t.Danger).Children(func() {
				riskyWarning(c, i18n.T("share.public"))
				a.copyable(c, i18n.T("share.link"), l.URL)
				ui.Row(c).Gap(8).Wrap().Children(func() {
					if labeledButton(c, iconExternal, i18n.T("open"), false).Clicked() {
						openURL(l.URL)
					}
					if labeledButton(c, iconStop, i18n.T("share.stop"), false).TextColor(t.Danger).Clicked() {
						a.m.StopPublicLink(s, f.Port.Remote)
					}
				})
			})
		case l != nil && l.Starting:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, i18n.T("share.starting")).FontSize(13).Grow(1)
				if ui.Button(c, i18n.T("cancel")).Clicked() {
					a.m.StopPublicLink(s, f.Port.Remote)
				}
			})
		default:
			if l != nil && l.Err != "" {
				ui.Text(c, l.Err).FontSize(12).TextColor(t.Danger)
			}
			if !a.m.Settings.Risky.PublicLinks {
				return
			}
			noKey := f.Port.Kind == plugins.PortOpenAI && (s.Result == nil || s.Result.APIKey == "")
			ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				b := labeledButton(c, iconGlobe, i18n.T("share.make"), false).TextColor(t.Danger).Disabled(noKey)
				if b.Clicked() {
					r := &a.runtimes
					r.shareSession, r.sharePort, r.shareName, r.shareConfirm = s.ID, f.Port.Remote, f.Port.Name, true
				}
				if noKey {
					ui.Text(c, i18n.T("share.needs_key")).FontSize(12).TextColor(t.Warning).Shrink(1)
				}
			})
		}
	})
}

// shareDialog asks, each time, before a port goes public.
func (a *App) shareDialog(c *ui.Context) {
	r := &a.runtimes
	if !r.shareConfirm {
		return
	}
	switch dangerConfirm(c, &r.shareConfirm, i18n.F("share.confirm_title", r.shareName), i18n.T("share.confirm_text"), i18n.T("cancel"), i18n.T("share.confirm")) {
	case 1:
		if s := a.m.Session(r.shareSession); s != nil {
			if err := a.m.StartPublicLink(s, r.sharePort); err != nil {
				a.toasts = append(a.toasts, ui.Toast{Title: err.Error(), Type: "error", Timeout: 5e9})
			}
		}
		r.shareSession = ""
	case 0:
		r.shareSession = ""
	}
}

// freeWeb reports whether the form's task is one used through a web
// page, refused on the account's free tier; risky whether it goes ahead
// with the setting on, after asking.
func (a *App) freeWeb(acct *core.Account) (blocked, risky bool) {
	if !acct.FreeTier() || !core.WebTask(plugins.Get(a.deploy.form.Plugin)) {
		return false, false
	}
	if a.m.Settings.Risky.FreeWebUI {
		return false, true
	}
	return true, false
}

// freeWebNotice says why a web task can't be launched on the free tier,
// with the way to the setting that allows it.
func (a *App) freeWebNotice(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("free-web")).Gap(6).Padding(10, 12).Radius(8).Border(1, t.Danger).Children(func() {
		riskyWarning(c, i18n.T("freeweb.title"))
		ui.Text(c, i18n.T("freeweb.text")).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if labeledButton(c, iconSettings, i18n.T("freeweb.enable"), false).Clicked() {
				a.page = pageSettings
			}
			if labeledButton(c, iconExternal, i18n.T("free.faq"), false).Clicked() {
				openURL(colabFAQ)
			}
		})
	})
}

// freeWebDialog asks, each launch, before a web task goes on the free tier.
func (a *App) freeWebDialog(c *ui.Context) {
	d := &a.deploy
	if !d.freeConfirm {
		return
	}
	if dangerConfirm(c, &d.freeConfirm, i18n.T("freeweb.confirm_title"), i18n.T("freeweb.confirm_text"), i18n.T("cancel"), i18n.T("freeweb.confirm")) == 1 {
		a.launch()
	}
}
