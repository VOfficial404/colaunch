// Package app is Colaunch's interface, in MyGo's native UI: one window
// that lays its pages out beside a sidebar on desktops, and above a bar of
// tabs on phones.
package app

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	"colaunch/internal/plugins"
)

// Version is the app's version, set at build time.
var Version = "dev"

// accent is Colab's orange, for icons and large areas. Text and primary
// buttons in the light theme take accentDeep, which reads on white.
var (
	accent     = ui.Hex("#e8710a")
	accentDeep = ui.Hex("#b45309")
)

const (
	pageDeploy   = "deploy"
	pageRuntimes = "runtimes"
	pageChat     = "chat"
	pageCreate   = "create"
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
	create   createState
	ports    portsState
	accounts accountsState
	settings settingsState
	risky    riskyState
	domains  domainsState
	tasks    tasksState
	// details are the errors whose details are open, by notice.
	details map[string]bool

	desktop          desktopBackground
	mobile           mobileBackground
	backgroundQueued bool
	quitting         bool
}

// Run runs the app until it quits.
func Run() error {
	if isDesktop() && !mygo.App.RequestSingleInstanceLock() {
		return nil
	}
	// Updates, in a window of native UI: the app needs no webview.
	mygo.Use(native.Plugin)
	mygo.App.SetLanguages(i18n.Languages...)
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return err
	}
	m := core.New(dir, newSecrets(dir))
	defer m.Close()
	a := &App{m: m, page: pageDeploy}
	if m.Settings.Language != "" {
		mygo.App.SetLocale(m.Settings.Language)
	}
	m.Lang = i18n.Lang
	a.deploy.init(m)
	// colaunch://import links, which open the task they name for review.
	mygo.App.OnOpenURL(a.onOpenURL)
	if isDesktop() && runtime.GOOS != "darwin" && Version != "dev" && !mygo.App.IsURLSchemeRegistered(plugins.DeepLinkScheme) {
		// Installers register the scheme; a portable copy does it here.
		if err := mygo.App.RegisterURLScheme(plugins.DeepLinkScheme); err != nil {
			log.Printf("register %s://: %v", plugins.DeepLinkScheme, err)
		}
	}
	if isDesktop() {
		mygo.App.OnSecondInstance(func([]string, string) {
			if a.win != nil {
				a.showDesktopWindow()
			}
		})
	}
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
		m.Post = func(fn func()) {
			a.win.Update(func() {
				fn()
				if !a.quitting {
					a.syncMobileBackground()
				}
			})
		}
		// Snapshots for the manager's own bookkeeping: on the main thread,
		// without redrawing the window.
		m.Peek = func(fn func()) { mygo.RunOnMain(fn) }
		m.Notify = a.notify
		a.initDesktopBackground()
		a.initMobileBackground()
		m.Start()
		if link := a.tasks.pendingLink; link != "" {
			a.tasks.pendingLink = ""
			a.win.Update(func() { a.openLink(link) })
		}
	})
	// What the Create page made lives until the app quits; what a crash
	// left goes now.
	if dir, err := mygo.App.Path(mygo.PathCache); err == nil {
		a.create.dir = filepath.Join(dir, "creations")
		os.RemoveAll(a.create.dir)
	}
	mygo.App.OnQuit(a.create.cleanup)
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.quitting = true })
	mygo.App.OnQuitCanceled(func() { a.quitting = false })
	mygo.App.OnLocaleChanged(func(tag string) {
		i18n.SetLang(tag)
		a.refreshDesktopBackgroundMenus()
	})
	return mygo.App.Run()
}

// notify shows a toast, from the main thread.
func (a *App) notify(kind, title, text string) {
	if kind == "info" && title != "" && text == "" {
		text = i18n.T("stopped")
	}
	// The manager names some of its messages by key.
	if key := "accounts." + title; i18n.Has(key) {
		title, text = i18n.T(key), ""
	}
	important := kind != "success"
	if kind == "grabbed" {
		kind, text, important = "success", i18n.F("grab.got", text), true
	}
	// Grabbing can take hours and a runtime can be reclaimed while the app is
	// in the background: say so outside the window too.
	if important && a.win != nil && !a.win.IsFocused() && mygo.NotificationsSupported() {
		mygo.NewNotification(mygo.NotificationOptions{Title: title, Body: text}).Show()
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
	a.queueBackgroundSync()
}

// Settings and tasks can change in a view's click handlers. Apply native
// background changes after the frame, without requesting another redraw.
func (a *App) queueBackgroundSync() {
	if a.win == nil || a.quitting || a.backgroundQueued {
		return
	}
	mobile := runtime.GOOS == "ios" || runtime.GOOS == "android"
	if !mobile && (!isDesktop() || a.desktop.enabled == a.m.Settings.BackgroundEnabled()) {
		return
	}
	a.backgroundQueued = true
	mygo.App.WhenReady(func() {
		a.backgroundQueued = false
		if a.quitting {
			return
		}
		if mobile {
			a.syncMobileBackground()
		} else {
			a.syncDesktopBackground()
		}
	})
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
	brand := accent
	if !t.Dark {
		brand = accentDeep
	}
	th := *t.WithAccent(brand)
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
	{pageCreate, iconSparkles, "nav.create"},
	{pagePorts, iconCable, "nav.ports"},
	{pageAccounts, iconUsers, "nav.accounts"},
	{pagePlugins, iconPuzzle, "nav.tasks"},
	{pageSettings, iconSettings, "nav.settings"},
}

// mobileNav are the tabs phones show; the others, settingsLinks, are
// under Settings. Accounts are set up once and switched seldom, so they
// leave their tab to Create.
var (
	mobileNav     = []string{pageDeploy, pageRuntimes, pageChat, pageCreate, pageSettings}
	settingsLinks = []string{pageAccounts, pagePorts, pagePlugins}
)

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
					if it.page == pageCreate && a.generating() > 0 {
						item.Children(func() { ui.Spinner(c) })
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
				active := a.page == p || (p == pageSettings && slices.Contains(settingsLinks, a.page))
				color := t.TextMuted
				if active {
					color = t.Accent
				}
				col := ui.Column(c.Key(p)).Grow(1).AlignItems(ui.Center).Gap(2).PaddingY(4).Focusable().Role(ui.RoleTab).Label(i18n.T(it.key)).AccessSelected(active)
				col.Children(func() {
					pill := ui.Row(c).Padding(2, 14).Radius(999)
					if active {
						pill.Background(accent.Alpha(0.16))
					}
					pill.Children(func() { ui.Icon(c, it.icon).FontSize(20).TextColor(color) })
					label := ui.Text(c, i18n.T(it.key)).FontSize(11).TextColor(color).SingleLine()
					if active {
						label.Bold()
					}
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
	case pageCreate:
		a.createPage(c)
	case pagePorts:
		a.portsPage(c)
	case pageAccounts:
		a.accountsPage(c)
	case pagePlugins:
		a.tasksPage(c)
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
	if acct == nil {
		box.Label(i18n.T("account.none"))
	} else {
		box.Label(i18n.F("account.footer_label", acct.Email, balanceLine(acct)))
	}
	box.Children(func() {
		ui.Text(c, i18n.T("account.current")).FontSize(11).Bold().TextColor(t.TextMuted)
		if acct == nil {
			ui.Text(c, i18n.T("account.none")).TextColor(t.TextMuted).FontSize(12)
			return
		}
		ui.Text(c, acct.Email).FontSize(12).SingleLine().Ellipsis("…")
		ui.Text(c, balanceLine(acct)).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
	})
	if box.Clicked() {
		a.page = pageAccounts
	}
}

// pageScroll builds a page's frame: a title, and content that scrolls.
func pageScroll(c *ui.Context, title string, actions func(), body func()) {
	pageFrame(c, title, nil, actions, body, nil)
}

// pageFrame is pageScroll with, where they aren't nil, a button back to
// Settings before the title, and a bar under the content that stays put
// as it scrolls.
func pageFrame(c *ui.Context, title string, back func(), actions func(), body func(), footer func()) {
	t := c.Theme()
	pad := pagePad(c)
	ui.Column(c).Fill().Children(func() {
		if back != nil {
			ui.Row(c).Padding(10, pad, 0, pad).Children(func() {
				if ui.Button(c, i18n.T("back.settings")).Label(i18n.T("back.settings_label")).Clicked() {
					back()
				}
			})
		}
		ui.Row(c).Padding(16, pad, 8, pad).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, title).FontSize(22).Bold().Grow(1)
			if actions != nil {
				actions()
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(8, pad, 32, pad).Gap(16).MaxWidth(980).FillWidth().Children(body)
		})
		if footer != nil {
			ui.Column(c).Padding(10, pad).Gap(8).Background(t.Surface).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(footer)
		}
	})
}

func pagePad(c *ui.Context) float32 {
	if isNarrow(c) {
		return 14
	}
	return 24
}

// formCols is how many columns a form's fields take: one on phones.
func formCols(c *ui.Context) int {
	if w, _ := c.Size(); w < 480 {
		return 1
	}
	return 2
}

// isNarrow reports whether the window is a phone's, without a sidebar.
func isNarrow(c *ui.Context) bool {
	w, _ := c.Size()
	return w < 720
}

// isPhone reports whether the window is as narrow as a phone held upright.
func isPhone(c *ui.Context) bool {
	w, _ := c.Size()
	return w < 480
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

// dangerConfirm is an alert whose last button, the one that does what
// can't be undone, is in the theme's Danger color.
func dangerConfirm(c *ui.Context, open *bool, title, text, cancel, confirm string) int {
	t := *c.Theme()
	black := ui.Hex("#000000")
	t.Accent, t.AccentHover, t.AccentPressed = t.Danger, t.Danger.Mix(black, 0.12), t.Danger.Mix(black, 0.25)
	t.AccentText = ui.Hex("#ffffff")
	t.Focus = t.Danger.Alpha(0.55)
	chosen := -1
	ui.Themed(c, &t).Size(0, 0).Children(func() {
		chosen = ui.AlertDialog(c, open, title, text, cancel, confirm)
	})
	return chosen
}

// undoable does what can be undone for a moment: hide hides it now, and
// do does it once the toast closes, unless its Undo was clicked, which
// runs restore.
func (a *App) undoable(title string, hide, restore, do func()) {
	hide()
	undone := false
	a.toasts = append(a.toasts, ui.Toast{
		Title: title, Type: "info", Timeout: 6e9,
		Action:   i18n.T("undo"),
		OnAction: func() { undone = true; restore() },
		OnClose: func() {
			if !undone {
				do()
			}
		},
	})
}

// isFreeTier reports whether Colab gives the account only its free tier:
// no subscription, or no compute units left.
func isFreeTier(acct *core.Account) bool { return acct.FreeTier() }

const colabFAQ = "https://research.google.com/colaboratory/faq.html#disallowed-activities"

// freeTierNotice says what Colab allows free runtimes, for free accounts.
func freeTierNotice(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("free-tier")).Gap(6).Padding(10, 12).Radius(8).Border(1, t.Warning).Children(func() {
		ui.Text(c, i18n.T("free.title")).Bold().FontSize(13)
		ui.Text(c, i18n.T("free.text")).FontSize(13).TextColor(t.TextMuted)
		ui.Row(c).Children(func() {
			if labeledButton(c, iconExternal, i18n.T("free.faq"), false).Clicked() {
				openURL(colabFAQ)
			}
		})
	})
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
