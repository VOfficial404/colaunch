package app

import (
	"fmt"
	"runtime"
	"time"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/core"
	"colaunch/internal/google"
	"colaunch/internal/i18n"
)

type accountsState struct {
	login    *core.Login
	paste    string
	err      string
	confirm  bool
	toRemove *core.Account
	// importFailed is set when no editor had the Colab extension's client.
	importFailed bool
}

func (a *App) startLogin() {
	s := &a.accounts
	s.err = ""
	if s.login != nil {
		s.login.Cancel()
	}
	l, err := a.m.StartLogin(func(acct *core.Account, err error) {
		s.login = nil
		if err != nil {
			s.err = err.Error()
			return
		}
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("account.added"), Description: acct.Email, Type: "success"})
	})
	if err != nil {
		s.err = err.Error()
		return
	}
	s.login = l
	s.paste = ""
	openURL(l.URL)
}

func (a *App) accountsPage(c *ui.Context) {
	s := &a.accounts
	t := c.Theme()
	hasClient := a.m.Client().Valid()
	pageScroll(c, i18n.T("nav.accounts"), func() {
		// Signing in needs a client: until there is one, the guide below
		// says what to do first.
		b := labeledButton(c, iconPlus, i18n.T("account.add"), hasClient && len(a.m.Accounts) > 0).Disabled(!hasClient)
		if !hasClient {
			b.Tooltip(i18n.T("client.needed"))
		}
		if b.Clicked() {
			a.startLogin()
		}
	}, func() {
		if !hasClient && len(a.m.Accounts) > 0 {
			ui.Text(c, i18n.T("client.needed")).FontSize(13).TextColor(t.Warning)
		}
		if s.login != nil {
			a.loginCard(c)
		}
		a.errorNotice(c, "login", s.err)
		if s.login == nil && (len(a.m.Accounts) == 0 || !hasClient) {
			a.onboarding(c)
		}
		if len(a.m.Accounts) == 0 {
			return
		}
		// Every account's balance at a glance.
		var total, rate float64
		for _, acct := range a.m.Accounts {
			if acct.Quota != nil {
				total += acct.Quota.Balance
				rate += acct.Quota.RateHourly
			}
		}
		if len(a.m.Accounts) > 1 {
			card(c, func() {
				ui.Row(c).Gap(12).Wrap().Children(func() {
					stat(c, fmt.Sprintf("%.2f", total), i18n.T("account.total"))
					stat(c, fmt.Sprintf("%.2f", rate), i18n.T("cost.rate"))
					stat(c, fmt.Sprint(len(a.m.Accounts)), i18n.T("nav.accounts"))
				})
			})
		}
		active := a.m.Active()
		for _, acct := range a.m.Accounts {
			ui.Column(c.Key(acct.Email)).Children(func() { a.accountCard(c, acct, acct == active) })
		}
	})
	if s.confirm && s.toRemove != nil {
		switch dangerConfirm(c, &s.confirm, i18n.T("account.remove_title"), i18n.F("account.remove_text", s.toRemove.Email), i18n.T("cancel"), i18n.T("remove")) {
		case 1:
			a.m.RemoveAccount(s.toRemove)
			s.toRemove = nil
		case 0:
			s.toRemove = nil
		}
	}
}

func (a *App) loginCard(c *ui.Context) {
	s := &a.accounts
	t := c.Theme()
	section(c, i18n.T("login.title"), func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, i18n.T("login.waiting"))
		})
		ui.Text(c, i18n.T("login.paste_help")).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			in := ui.TextInput(c, &s.paste).Placeholder(i18n.F("form.example", "http://127.0.0.1:…/?state=…&code=…")).Grow(1).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			if ui.Button(c, i18n.T("login.submit")).Clicked() || in.Submitted() {
				if err := s.login.Paste(s.paste); err != nil {
					s.err = i18n.T("login.paste_invalid")
				} else {
					// The address holds the code: don't keep it around.
					s.paste = ""
				}
			}
		})
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if labeledButton(c, iconExternal, i18n.T("login.open_again"), false).Clicked() {
				openURL(s.login.URL)
			}
			if labeledButton(c, iconCopy, i18n.T("login.copy_link"), false).Clicked() {
				a.copyText(s.login.URL)
			}
			if ui.Button(c, i18n.T("cancel")).Clicked() {
				s.login.Cancel()
				s.login = nil
			}
		})
	})
}

func (a *App) importClient() {
	cl, dir, err := google.FindEditorClient()
	if err != nil {
		a.accounts.importFailed = true
		a.toasts = append(a.toasts, ui.Toast{
			Title: i18n.T("client.not_found"), Description: i18n.T("client.not_found_text"), Type: "error",
			Action: i18n.T("client.manual"), OnAction: func() { a.page = pageSettings },
		})
		return
	}
	a.accounts.importFailed = false
	a.m.Settings.OAuth = cl
	a.m.SaveSettings()
	a.settings.loaded = false
	a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("client.imported"), Description: dir, Type: "success"})
}

func isDesktop() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func (a *App) accountCard(c *ui.Context, acct *core.Account, active bool) {
	s := &a.accounts
	t := c.Theme()
	el := card(c, func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			name := acct.Name
			if name == "" {
				name = acct.Email
			}
			ui.Avatar(c, name, nil).Size(40, 40)
			ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, name).Bold().SingleLine().Ellipsis("…").Shrink(1)
					if acct.Quota != nil && acct.Quota.Tier != "" {
						ui.Badge(c, tierName(acct.Quota.Tier))
					}
					if active {
						ui.Text(c, i18n.T("account.active")).FontSize(12).TextColor(t.Accent).Bold()
					}
				})
				ui.Text(c, acct.Email).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			})
			if acct.Refreshing {
				ui.Spinner(c)
			}
		})
		if acct.SignedOut {
			ui.Text(c, i18n.T("account.signed_out")).TextColor(t.Warning).FontSize(13)
		} else if acct.QuotaErr != "" {
			a.errorNotice(c, "quota-"+acct.Email, acct.QuotaErr)
		}
		if isFreeTier(acct) {
			freeTierNotice(c)
		}
		if q := acct.Quota; q != nil {
			ui.Row(c).Gap(12).Wrap().Children(func() {
				stat(c, fmt.Sprintf("%.2f", q.Balance), i18n.T("cost.balance"))
				stat(c, fmt.Sprintf("%.2f", q.RateHourly), i18n.T("cost.rate"))
				left := "—"
				if h := q.HoursLeft(); h >= 0 {
					left = duration(time.Duration(h * float64(time.Hour)))
				}
				stat(c, left, i18n.T("cost.left"))
				stat(c, fmt.Sprint(q.Assignments), i18n.T("account.runtimes"))
			})
			if free := q.FreeRemaining(); free >= 0 {
				line := i18n.F("account.free", free)
				if q.FreeCCU != nil && q.FreeCCU.NextRefill > 0 {
					line += " · " + i18n.F("account.refill", time.Unix(q.FreeCCU.NextRefill, 0).Format("01-02 15:04"))
				}
				ui.Text(c, line).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Text(c, i18n.F("account.updated", q.FetchedAt.Format("15:04:05"))).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if !active {
				if labeledButton(c, iconCheck, i18n.T("account.use"), true).Clicked() {
					a.m.SetActive(acct.Email)
				}
			}
			if acct.SignedOut {
				if ui.PrimaryButton(c, i18n.T("account.sign_in_again")).Disabled(!a.m.Client().Valid()).Clicked() {
					a.startLogin()
				}
			}
			if iconButton(c, iconRefresh, i18n.T("refresh")).Clicked() {
				a.m.Refresh(acct)
			}
			ui.Spacer(c)
			if labeledButton(c, iconTrash, i18n.T("remove"), false).Clicked() {
				s.toRemove, s.confirm = acct, true
			}
		})
	})
	if active {
		el.Border(2, t.Accent)
	}
}

func tierName(tier string) string {
	switch tier {
	case "PRO":
		return "Colab Pro"
	case "PRO_PLUS":
		return "Colab Pro+"
	case "NONE", "FREE", "UNSPECIFIED":
		return i18n.T("tier.free")
	}
	return tier
}

// balanceLine is an account's balance in a line.
func balanceLine(acct *core.Account) string {
	q := acct.Quota
	if q == nil {
		if acct.QuotaErr != "" {
			return shortError(acct.QuotaErr)
		}
		return i18n.T("loading")
	}
	line := i18n.F("account.balance_line", q.Balance, q.RateHourly)
	if h := q.HoursLeft(); h >= 0 {
		line += " · " + i18n.F("account.lasts", duration(time.Duration(h*float64(time.Hour))))
	}
	return line
}
