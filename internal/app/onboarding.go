package app

import (
	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
)

const docsOAuth = "https://github.com/VOfficial404/colaunch/blob/main/docs/oauth.md"

// onboardingStep is the first step of getting started not done yet: 0
// connect an OAuth client, 1 sign in with Google, 2 deploy a first model,
// 3 all done.
func (a *App) onboardingStep() int {
	switch {
	case !a.m.Client().Valid():
		return 0
	case len(a.m.Accounts) == 0:
		return 1
	case len(a.m.Sessions) == 0:
		return 2
	}
	return 3
}

// onboarding walks a new user through the three steps of getting
// started. Only the step they are at has a primary button; the Accounts
// page and the Deploy page show the same card.
func (a *App) onboarding(c *ui.Context) {
	t := c.Theme()
	at := a.onboardingStep()
	steps := []struct{ title, text string }{
		{"onboard.client", "onboard.client_text"},
		{"onboard.login", "onboard.login_text"},
		{"onboard.deploy", "onboard.deploy_text"},
	}
	card(c.Key("onboarding"), func() {
		ui.Text(c, i18n.T("account.welcome")).FontSize(17).Bold()
		ui.Text(c, i18n.T("onboard.title")).TextColor(t.TextMuted).FontSize(13)
		for i, st := range steps {
			done, current := i < at, i == at
			ui.Row(c.Key(st.title)).Gap(12).AlignItems(ui.Start).Children(func() {
				// The step's number, or a check once it's done.
				dot := ui.Column(c).Size(28, 28).Radius(999).Center()
				switch {
				case done:
					dot.Background(t.Success).Children(func() {
						ui.Icon(c, iconCheck).FontSize(16).TextColor(ui.Hex("#ffffff"))
					})
				case current:
					dot.Background(t.Accent).Children(func() {
						ui.Text(c, itoa(i+1)).Bold().TextColor(t.AccentText)
					})
				default:
					dot.Border(1, t.Border).Children(func() {
						ui.Text(c, itoa(i+1)).TextColor(t.TextMuted)
					})
				}
				ui.Column(c).Gap(6).Grow(1).Shrink(1).Children(func() {
					title := ui.Text(c, i18n.T(st.title)).Bold()
					if !current {
						title.TextColor(t.TextMuted)
					}
					if done {
						ui.Text(c, i18n.T("onboard.done")).FontSize(12).TextColor(t.Success)
						return
					}
					if !current {
						return
					}
					ui.Text(c, i18n.T(st.text)).FontSize(13).TextColor(t.TextMuted)
					switch i {
					case 0:
						a.clientActions(c, true)
					case 1:
						ui.Row(c).Gap(8).Wrap().Children(func() {
							if labeledButton(c, iconPlus, i18n.T("onboard.login"), true).Clicked() {
								a.page = pageAccounts
								a.startLogin()
							}
						})
					case 2:
						if a.page != pageDeploy {
							ui.Row(c).Children(func() {
								if labeledButton(c, iconRocket, i18n.T("onboard.go_deploy"), true).Clicked() {
									a.page = pageDeploy
								}
							})
						}
					}
				})
			})
		}
	})
}

// clientActions are the ways to get an OAuth client: importing the Colab
// extension's from an editor, on desktops, or entering one by hand; and
// why importing failed, with what to do instead. primary makes the first
// of them the primary button.
func (a *App) clientActions(c *ui.Context, primary bool) {
	t := c.Theme()
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if isDesktop() {
			if labeledButton(c, iconImport, i18n.T("client.import"), primary).Clicked() {
				a.importClient()
			}
		}
		if labeledButton(c, iconSettings, i18n.T("client.manual"), primary && !isDesktop()).Clicked() {
			a.page = pageSettings
		}
		if labeledButton(c, iconExternal, i18n.T("client.docs"), false).Clicked() {
			openURL(docsOAuth)
		}
	})
	if a.accounts.importFailed {
		ui.Column(c).Gap(4).Padding(10, 12).Radius(8).Border(1, t.Warning).Children(func() {
			ui.Text(c, i18n.T("client.not_found")).Bold().FontSize(13).TextColor(t.Warning)
			ui.Text(c, i18n.T("client.not_found_text")).FontSize(13)
		})
	}
}
