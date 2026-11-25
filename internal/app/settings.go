package app

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

type settingsState struct {
	loaded   bool
	clientID string
	secret   string
	hfToken  string
	hfURL    string
	cudaURL  string
	attempts float64
}

func (a *App) pluginsPage(c *ui.Context) {
	t := c.Theme()
	pageScroll(c, i18n.T("nav.plugins"), func() {
		if iconButton(c, iconRefresh, i18n.T("plugins.reload")).Clicked() {
			a.m.ReloadPlugins()
		}
	}, func() {
		ui.Text(c, i18n.T("plugins.help")).TextColor(t.TextMuted).FontSize(13)
		for _, p := range plugins.All() {
			card(c.Key(p.ID()), func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconPuzzle).TextColor(accent)
					ui.Text(c, p.Name().In(i18n.Lang())).Bold().Grow(1)
					if sp, ok := p.(*plugins.ScriptPlugin); ok {
						ui.Badge(c, i18n.T("plugins.user"))
						if iconButton(c, iconFolder, i18n.T("plugins.open_folder")).Clicked() {
							mygo.Shell.OpenPath(sp.Dir)
						}
					} else {
						ui.Badge(c, i18n.T("plugins.builtin"))
					}
				})
				ui.Text(c, p.Description().In(i18n.Lang())).FontSize(13).TextColor(t.TextMuted)
				if ui.Button(c, i18n.T("plugins.use")).Clicked() {
					a.deploy.form.Plugin = p.ID()
					a.page = pageDeploy
				}
			})
		}
		for _, err := range a.m.PluginErrors {
			ui.Text(c, err.Error()).TextColor(t.Danger).FontSize(12).Selectable()
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if isDesktop() {
				if labeledButton(c, iconFolder, i18n.T("plugins.open_folder"), false).Clicked() {
					dir := a.m.PluginsDir()
					a.writeExample(dir)
					mygo.Shell.OpenPath(dir)
				}
			}
			if ui.Button(c, i18n.T("plugins.docs")).Clicked() {
				openURL("https://github.com/VOfficial404/colaunch/blob/main/docs/plugins.md")
			}
		})
		a.copyable(c, i18n.T("plugins.folder"), a.m.PluginsDir())
	})
}

// writeExample puts the example plugin in the plugins folder when it has
// none, to start from.
func (a *App) writeExample(dir string) {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return
	}
	ex := filepath.Join(dir, "jupyter-lab")
	if err := os.MkdirAll(ex, 0o755); err != nil {
		return
	}
	for name, body := range plugins.Example {
		os.WriteFile(filepath.Join(ex, name), []byte(body), 0o644)
	}
	a.m.ReloadPlugins()
}

var themes = []string{"system", "light", "dark"}

func (a *App) settingsPage(c *ui.Context) {
	s := &a.settings
	set := &a.m.Settings
	if !s.loaded {
		s.loaded = true
		s.clientID, s.secret = set.OAuth.ID, set.OAuth.Secret
		s.hfToken = a.m.HFToken()
		s.hfURL, s.cudaURL = set.HFEndpoint, set.LlamaCUDAURL
		s.attempts = float64(set.MaxAttempts)
	}
	t := c.Theme()
	pageScroll(c, i18n.T("nav.settings"), nil, func() {
		w, _ := c.Size()
		if w < 720 {
			// Phones have no sidebar: the other pages are here.
			card(c, func() {
				for _, p := range []string{pagePorts, pagePlugins} {
					it := navFor(p)
					if labeledButton(c, it.icon, i18n.T(it.key), false).Clicked() {
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
				switchRow(c, &enabled, i18n.T("background.enabled"))
				if enabled != set.BackgroundEnabled() {
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

		section(c, i18n.T("client.title"), func() {
			ui.Text(c, i18n.T("client.text")).FontSize(12).TextColor(t.TextMuted)
			ui.Field(c, "Client ID", func() {
				ui.TextInput(c, &s.clientID).Placeholder("1234-abc.apps.googleusercontent.com").Autocorrect(false).Capitalize(ui.CapitalizeNone)
			})
			ui.Field(c, "Client secret", func() {
				ui.TextInput(c, &s.secret).Password().Placeholder("GOCSPX-…")
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if ui.PrimaryButton(c, i18n.T("save")).Clicked() {
					set.OAuth.ID, set.OAuth.Secret = s.clientID, s.secret
					a.m.SaveSettings()
					a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("saved"), Type: "success", Timeout: 1500e6})
				}
				if isDesktop() {
					if ui.Button(c, i18n.T("client.import")).Clicked() {
						a.importClient()
					}
				}
			})
			if !set.OAuth.Valid() && a.m.Client().Valid() {
				ui.Text(c, i18n.T("client.builtin")).FontSize(12).TextColor(t.TextMuted)
			}
		})

		section(c, "Hugging Face", func() {
			ui.Field(c, i18n.T("settings.hf_token"), func() {
				ui.TextInput(c, &s.hfToken).Password().Placeholder("hf_…")
			})
			ui.Text(c, i18n.T("settings.hf_token_help")).FontSize(12).TextColor(t.TextMuted)
			ui.Field(c, i18n.T("settings.hf_endpoint"), func() {
				ui.TextInput(c, &s.hfURL).Placeholder("https://huggingface.co").Autocorrect(false).Capitalize(ui.CapitalizeNone)
			})
			if ui.PrimaryButton(c, i18n.T("save")).Clicked() {
				a.m.SetHFToken(s.hfToken)
				set.HFEndpoint = s.hfURL
				a.m.SaveSettings()
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("saved"), Type: "success", Timeout: 1500e6})
			}
		})

		section(c, i18n.T("settings.advanced"), func() {
			ui.Field(c, i18n.T("settings.cuda_url"), func() {
				ui.TextInput(c, &s.cudaURL).Placeholder(plugins.DefaultCUDAURL).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			})
			ui.Field(c, i18n.T("settings.attempts"), func() {
				ui.NumberInput(c, &s.attempts, 1, 30, 1).Width(160)
			})
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Switch(c, &set.KeepFailed).Label(i18n.T("settings.keep_failed"))
				ui.Text(c, i18n.T("settings.keep_failed")).Grow(1).Shrink(1)
			})
			if ui.PrimaryButton(c, i18n.T("save")).Clicked() {
				set.LlamaCUDAURL = s.cudaURL
				set.MaxAttempts = int(s.attempts)
				a.m.SaveSettings()
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("saved"), Type: "success", Timeout: 1500e6})
			}
		})

		section(c, i18n.T("settings.about"), func() {
			ui.Text(c, "Colaunch "+appVersion()).Bold()
			ui.Text(c, i18n.T("settings.about_text")).FontSize(13).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if isDesktop() && labeledButton(c, iconRefresh, i18n.T("settings.check_updates"), false).Clicked() {
					updater.CheckForUpdates()
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
	})
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
