package app

import (
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

type settingsState struct {
	loaded   bool
	clientID string
	secret   string
	hfToken  string
	// hfSaved is the token as saved, read once: it is in the keychain.
	hfSaved  string
	hfURL    string
	cudaURL  string
	attempts float64
}

var themes = []string{"system", "light", "dark"}

// saveRow ends a section whose text waits to be saved: its Save button,
// on the right, which does nothing until something changed. Everywhere in
// Settings, choices (menus, switches) take effect at once, and what is
// typed waits for its section's Save.
func (a *App) saveRow(c *ui.Context, dirty bool, save func()) {
	ui.Row(c).Gap(8).Children(func() {
		ui.Spacer(c)
		if ui.PrimaryButton(c, i18n.T("save")).Disabled(!dirty).Clicked() {
			save()
			a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("saved"), Type: "success", Timeout: 1500e6})
		}
	})
}

func (a *App) settingsPage(c *ui.Context) {
	s := &a.settings
	set := &a.m.Settings
	if !s.loaded {
		s.loaded = true
		s.clientID, s.secret = set.OAuth.ID, set.OAuth.Secret
		s.hfToken = a.m.HFToken()
		s.hfSaved = s.hfToken
		s.hfURL, s.cudaURL = set.HFEndpoint, set.LlamaCUDAURL
		s.attempts = float64(set.MaxAttempts)
	}
	t := c.Theme()
	pageScroll(c, i18n.T("nav.settings"), nil, func() {
		if isNarrow(c) && a.updateOffered() {
			a.updateCard(c)
		}
		if isNarrow(c) {
			// Phones have no sidebar: the other pages are here.
			card(c, func() {
				for _, p := range settingsLinks {
					it := navFor(p)
					row := ui.Row(c.Key("link-" + p)).Gap(10).AlignItems(ui.Center).PaddingY(6).Focusable().Role(ui.RoleButton).Label(i18n.T(it.key))
					row.Children(func() {
						ui.Icon(c, it.icon).TextColor(accent)
						ui.Text(c, i18n.T(it.key)).Grow(1)
						ui.Icon(c, iconChevronRight).TextColor(t.TextMuted)
					})
					if row.Clicked() {
						a.page = p
					}
				}
			})
		}
		section(c, i18n.T("settings.appearance"), func() {
			langs := append([]string{i18n.T("settings.system")}, autonyms()...)
			chosen := langs[0]
			for i, tag := range i18n.Languages {
				if set.Language == tag {
					chosen = langs[i+1]
				}
			}
			ui.Field(c, i18n.T("settings.language"), func() {
				if ui.Select(c, &chosen, langs).Changed() {
					set.Language = ""
					for i, l := range langs[1:] {
						if l == chosen {
							set.Language = i18n.Languages[i]
						}
					}
					mygo.App.SetLocale(set.Language)
					i18n.SetLang(mygo.App.Locale())
					a.m.SaveSettings()
				}
			})
			ti := 0
			for i, th := range themes {
				if th == set.Theme {
					ti = i
				}
			}
			ui.Field(c, i18n.T("settings.theme"), func() {
				if ui.Segmented(c, &ti, i18n.T("settings.system"), i18n.T("settings.light"), i18n.T("settings.dark")).Changed() {
					set.Theme = themes[ti]
					a.m.SaveSettings()
				}
			})
		})

		if isDesktop() || runtime.GOOS == "android" || runtime.GOOS == "ios" {
			section(c, i18n.T("background.title"), func() {
				enabled := set.BackgroundEnabled()
				if switchRow(c, &enabled, i18n.T("background.enabled")) {
					set.Background = &enabled
					a.m.SaveSettings()
				}
				help := "background.desktop_help"
				switch runtime.GOOS {
				case "android":
					help = "background.android_help"
				case "ios":
					help = "background.ios_help"
				}
				ui.Text(c, i18n.T(help)).FontSize(12).TextColor(t.TextMuted)
			})
		}

		a.notificationsSection(c)

		section(c, i18n.T("client.title"), func() {
			ui.Text(c, i18n.T("client.text")).FontSize(12).TextColor(t.TextMuted)
			ui.Field(c, i18n.T("client.id"), func() {
				ui.TextInput(c, &s.clientID).Placeholder(example("1234-abc.apps.googleusercontent.com")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			})
			ui.Field(c, i18n.T("client.secret"), func() {
				ui.TextInput(c, &s.secret).Password().Placeholder(example("GOCSPX-…"))
			})
			if !set.OAuth.Valid() && a.m.Client().Valid() {
				ui.Text(c, i18n.T("client.builtin")).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
				if isDesktop() {
					if labeledButton(c, iconImport, i18n.T("client.import"), false).Clicked() {
						a.importClient()
					}
				}
				if labeledButton(c, iconExternal, i18n.T("client.docs"), false).Clicked() {
					openURL(docsOAuth)
				}
				ui.Spacer(c)
				if ui.PrimaryButton(c, i18n.T("save")).Disabled(s.clientID == set.OAuth.ID && s.secret == set.OAuth.Secret).Clicked() {
					set.OAuth.ID, set.OAuth.Secret = s.clientID, s.secret
					a.m.SaveSettings()
					a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("saved"), Type: "success", Timeout: 1500e6})
				}
			})
			if a.accounts.importFailed {
				ui.Text(c, i18n.T("client.not_found_text")).FontSize(12).TextColor(t.Warning)
			}
		})

		section(c, "Hugging Face", func() {
			ui.Field(c, i18n.T("settings.hf_token"), func() {
				ui.TextInput(c, &s.hfToken).Password().Placeholder(example("hf_…"))
			})
			ui.Text(c, i18n.T("settings.hf_token_help")).FontSize(12).TextColor(t.TextMuted)
			ui.Field(c, i18n.T("settings.hf_endpoint"), func() {
				ui.TextInput(c, &s.hfURL).Placeholder(example("https://huggingface.co")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			})
			ui.Text(c, i18n.T("settings.hf_mirror_no_token")).FontSize(12).TextColor(t.TextMuted)
			if u := strings.TrimSpace(s.hfURL); u != "" && !strings.HasPrefix(u, "https://") {
				ui.Text(c, i18n.T("settings.hf_endpoint_https")).FontSize(12).TextColor(t.Danger)
			}
			a.saveRow(c, s.hfToken != s.hfSaved || s.hfURL != set.HFEndpoint, func() {
				a.m.SetHFToken(s.hfToken)
				s.hfSaved = s.hfToken
				set.HFEndpoint = s.hfURL
				a.m.SaveSettings()
			})
		})

		section(c, i18n.T("settings.advanced"), func() {
			if switchRow(c, &set.KeepFailed, i18n.T("settings.keep_failed")) {
				a.m.SaveSettings()
			}
			ui.Field(c, i18n.T("settings.cuda_url"), func() {
				ui.TextInput(c, &s.cudaURL).Placeholder(example(plugins.DefaultCUDAURL)).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			})
			if strings.TrimSpace(s.cudaURL) != "" {
				ui.Text(c, i18n.T("settings.llama_cuda_unverified")).FontSize(12).TextColor(t.Warning)
			}
			ui.Field(c, i18n.T("settings.attempts"), func() {
				ui.NumberInput(c, &s.attempts, 1, 30, 1).Width(160)
			})
			a.saveRow(c, s.cudaURL != set.LlamaCUDAURL || int(s.attempts) != set.MaxAttempts, func() {
				set.LlamaCUDAURL = s.cudaURL
				set.MaxAttempts = int(s.attempts)
				a.m.SaveSettings()
			})
		})

		section(c, i18n.T("settings.about"), func() {
			ui.Text(c, "Colaunch "+appVersion()).Bold()
			ui.Text(c, i18n.T("settings.about_text")).FontSize(13).TextColor(t.TextMuted)
			a.updateAbout(c)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if labeledButton(c, iconRefresh, i18n.T("settings.check_updates"), false).Disabled(a.update.checking).Clicked() {
					a.checkUpdates(true)
				}
				if labeledButton(c, iconExternal, "GitHub", false).Clicked() {
					openURL("https://github.com/VOfficial404/colaunch")
				}
				if isDesktop() {
					if labeledButton(c, iconFolder, i18n.T("settings.data_folder"), false).Clicked() {
						mygo.Shell.OpenPath(a.m.Dir)
					}
				}
			})
		})

		a.riskySection(c)
	})
	a.riskyDialog(c)
	a.domainsDialogs(c)
}

func autonyms() []string {
	out := make([]string, len(i18n.Languages))
	for i, tag := range i18n.Languages {
		out[i] = i18n.Autonym(tag)
	}
	return out
}

func (a *App) copyText(s string) {
	mygo.Clipboard.WriteText(s)
	a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("copied"), Type: "success", Timeout: 1500e6})
}
