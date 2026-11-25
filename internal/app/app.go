// Package app is Colaunch's interface, in MyGo's native UI: one window
// that lays its pages out beside a sidebar on desktops, and above a bar of
// tabs on phones.
package app

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	// MyGo's own controls, in Chinese.
	_ "github.com/egoist/mygo/locales/zh-Hans"
	_ "github.com/egoist/mygo/locales/zh-Hant"

	"colaunch/internal/core"
	"colaunch/internal/i18n"
)

// Version is the app's version, set at build time.
var Version = "dev"

// accent is Colab's orange.
var accent = ui.Hex("#e8710a")

const (
	pageDeploy   = "deploy"
	pageRuntimes = "runtimes"
	pageChat     = "chat"
	pagePorts    = "ports"
	pageAccounts = "accounts"
	pagePlugins  = "plugins"
	pageSettings = "settings"
)

// App is the window's state.
type App struct {
	m      *core.Manager
	win    *mygo.Window
	page   string
	toasts []ui.Toast

	deploy   deployState
	runtimes runtimesState
	chat     chatState
	ports    portsState
	accounts accountsState
	settings settingsState
}

// Run runs the app until it quits.
func Run() error {
	// Updates, in a window of native UI: the app needs no webview.
	mygo.Use(native.Plugin)
	mygo.App.SetLanguages(i18n.Languages...)
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return err
	}
	m := core.New(dir, newSecrets(dir))
	a := &App{m: m, page: pageDeploy}
	if m.Settings.Language != "" {
		mygo.App.SetLocale(m.Settings.Language)
	}
	m.Lang = i18n.Lang
	a.deploy.init(m)
	mygo.App.WhenReady(func() {
		i18n.SetLang(mygo.App.Locale())
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Colaunch",
			Width:     1120,
			Height:    780,
			MinWidth:  360,
			MinHeight: 540,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
		m.Post = func(fn func()) { a.win.Update(fn) }
		m.Notify = a.notify
		m.Start()
	})
	mygo.App.OnLocaleChanged(func(tag string) { i18n.SetLang(tag) })
	err = mygo.App.Run()
	m.Close()
	return err
}

// notify shows a toast, from the main thread.
func (a *App) notify(kind, title, text string) {
	if kind == "info" && title != "" && text == "" {
		text = i18n.T("stopped")
	}
	a.toasts = append(a.toasts, ui.Toast{Title: title, Description: text, Type: kind})
}

func (a *App) view(c *ui.Context) {
	a.theme(c)
	for _, t := range a.toasts {
		c.AddToast(t)
	}
	a.toasts = nil
	w, _ := c.Size()
	if w < 720 {
		a.narrow(c)
	} else {
		a.wide(c)
	}
}

func (a *App) theme(c *ui.Context) {
	var t *ui.Theme
	switch a.m.Settings.Theme {
	case "light":
		t = ui.LightTheme()
	case "dark":
		t = ui.DarkTheme()
	default:
		t = c.Theme()
	}
	th := *t.WithAccent(accent)
	th.Radius = 8
	c.SetTheme(&th)
}

type navItem struct {
	page string
	icon *ui.SVG
	key  string
}

var navItems = []navItem{
	{pageDeploy, iconRocket, "nav.deploy"},
	{pageRuntimes, iconServer, "nav.runtimes"},
	{pageChat, iconChat, "nav.chat"},
	{pagePorts, iconCable, "nav.ports"},
	{pageAccounts, iconUsers, "nav.accounts"},
	{pagePlugins, iconPuzzle, "nav.plugins"},
	{pageSettings, iconSettings, "nav.settings"},
}

// mobileNav are the tabs phones show; the others are under Settings.
var mobileNav = []string{pageDeploy, pageRuntimes, pageChat, pageAccounts, pageSettings}

func (a *App) wide(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(220).Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).Gap(8).Padding(18, 16, 10, 16).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconRocket).FontSize(20).TextColor(accent)
				ui.Text(c, "Colaunch").FontSize(18).Bold()
			})
			ui.Sidebar(c, &a.page, func() {
				for _, it := range navItems {
					item := ui.SidebarItem(c, it.page, it.icon, i18n.T(it.key))
					if it.page == pageRuntimes {
						if n := len(a.m.Live()); n > 0 {
							item.Children(func() { ui.Badge(c, itoa(n)) })
						}
					}
				}
			}).Grow(1)
			a.accountFooter(c)
		})
		ui.Column(c).Grow(1).Children(func() { a.content(c) })
	})
}

func (a *App) narrow(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Grow(1).Children(func() { a.content(c) })
		ui.Row(c).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Background(t.Surface).PaddingY(4).Children(func() {
			for _, p := range mobileNav {
				it := navFor(p)
				active := a.page == p || (p == pageSettings && (a.page == pagePorts || a.page == pagePlugins))
				color := t.TextMuted
				if active {
					color = accent
				}
				col := ui.Column(c.Key(p)).Grow(1).AlignItems(ui.Center).Gap(2).PaddingY(4).Focusable().Role(ui.RoleTab).Label(i18n.T(it.key))
				col.Children(func() {
					ui.Icon(c, it.icon).FontSize(20).TextColor(color)
					ui.Text(c, i18n.T(it.key)).FontSize(11).TextColor(color).SingleLine()
				})
				if col.Clicked() {
					a.page = p
				}
			}
		})
	})
}

func navFor(page string) navItem {
	for _, it := range navItems {
		if it.page == page {
			return it
		}
	}
	return navItems[0]
}

func (a *App) content(c *ui.Context) {
	switch a.page {
	case pageDeploy:
		a.deployPage(c)
	case pageRuntimes:
		a.runtimesPage(c)
	case pageChat:
		a.chatPage(c)
	case pagePorts:
		a.portsPage(c)
	case pageAccounts:
		a.accountsPage(c)
	case pagePlugins:
		a.pluginsPage(c)
	case pageSettings:
		a.settingsPage(c)
	}
}

// accountFooter shows the active account and its balance under the
// sidebar.
func (a *App) accountFooter(c *ui.Context) {
	t := c.Theme()
	acct := a.m.Active()
	box := ui.Column(c).Padding(12, 14).Gap(2).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Focusable().Role(ui.RoleButton)
	box.Children(func() {
		if acct == nil {
			ui.Text(c, i18n.T("account.none")).TextColor(t.TextMuted).FontSize(12)
			return
		}
		ui.Text(c, acct.Email).FontSize(12).SingleLine().Ellipsis("…")
		ui.Text(c, balanceLine(acct)).FontSize(12).TextColor(t.TextMuted).SingleLine()
	})
	if box.Clicked() {
		a.page = pageAccounts
	}
}

// pageScroll builds a page's frame: a title, and content that scrolls.
func pageScroll(c *ui.Context, title string, actions func(), body func()) {
	t := c.Theme()
	pad := float32(24)
	if w, _ := c.Size(); w < 720 {
		pad = 14
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, pad, 8, pad).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, title).FontSize(22).Bold().Grow(1)
			if actions != nil {
				actions()
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(8, pad, 32, pad).Gap(16).MaxWidth(980).FillWidth().Children(body)
		})
	})
	_ = t
}

// card is a box of related things.
func card(c *ui.Context, fn func()) ui.Element {
	t := c.Theme()
	return ui.Column(c).Padding(16).Gap(12).Radius(12).Background(t.Surface).Border(1, t.Border).Children(fn)
}

// section is a card with a heading.
func section(c *ui.Context, title string, fn func()) ui.Element {
	return card(c, func() {
		ui.Text(c, title).Bold().FontSize(15)
		fn()
	})
}

// stat is a number with its label below.
func stat(c *ui.Context, value, label string) {
	t := c.Theme()
	ui.Column(c).Gap(2).MinWidth(120).Grow(1).Children(func() {
		ui.Text(c, value).FontSize(20).Bold().SingleLine()
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted)
	})
}

// copyable shows a value with a button that copies it.
func (a *App) copyable(c *ui.Context, label, value string) {
	t := c.Theme()
	ui.Row(c.Key(label + value)).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).TextColor(t.TextMuted).Width(110).FontSize(13)
		ui.Text(c, value).Font("monospace").FontSize(13).Selectable().Grow(1).Shrink(1)
		if iconButton(c, iconCopy, i18n.T("copy")).Clicked() {
			mygo.Clipboard.WriteText(value)
			a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("copied"), Type: "success", Timeout: 1500e6})
		}
	})
}

// iconButton is a button that shows only an icon.
func iconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(label)
	b.Children(func() { ui.Icon(c, icon) })
	return b
}

// labeledButton is a button with an icon before its label.
func labeledButton(c *ui.Context, icon *ui.SVG, label string, primary bool) ui.Element {
	var b ui.Element
	if primary {
		b = ui.PrimaryButton(c, "")
	} else {
		b = ui.Button(c, "")
	}
	b.Label(label).Children(func() {
		ui.Icon(c, icon)
		ui.Text(c, label).SingleLine()
	})
	return b
}

func openURL(u string) {
	if err := mygo.Shell.OpenExternal(u); err != nil {
		log.Printf("open %s: %v", u, err)
	}
}

// secrets keeps secrets in the system's credential store, or, where there
// is none, as on Linux without a keyring, in a file only the user reads.
type secrets struct {
	file string
	mu   sync.Mutex
}

func newSecrets(dir string) *secrets { return &secrets{file: filepath.Join(dir, "secrets.json")} }

func (s *secrets) Get(key string) ([]byte, error) {
	b, err := mygo.Secrets.Get(key)
	if err == nil {
		return b, nil
	}
	if errors.Is(err, mygo.ErrSecretNotFound) {
		// It may have been kept in the file before.
		if v, ok := s.load()[key]; ok {
			return []byte(v), nil
		}
		return nil, err
	}
	if v, ok := s.load()[key]; ok {
		return []byte(v), nil
	}
	return nil, err
}

func (s *secrets) Set(key string, value []byte) error {
	if err := mygo.Secrets.Set(key, value); err == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	m[key] = string(value)
	return writeSecretFile(s.file, m)
}

func (s *secrets) Delete(key string) error {
	mygo.Secrets.Delete(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	if _, ok := m[key]; ok {
		delete(m, key)
		return writeSecretFile(s.file, m)
	}
	return nil
}

func (s *secrets) load() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(s.file)
	if err == nil {
		jsonUnmarshal(b, &m)
	}
	return m
}

func itoa(n int) string { return strings.TrimSpace(fmtInt(n)) }
