package app

import (
	"context"
	_ "embed"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"colaunch/internal/i18n"
)

//go:embed tray.png
var desktopTrayIcon []byte

// desktopBackground is owned by the main thread.
type desktopBackground struct {
	enabled        bool
	tray           *mygo.Tray
	releaseRunning func()
	stopWatching   context.CancelFunc
	fallbackMenu   bool
}

func (a *App) initDesktopBackground() {
	if !isDesktop() {
		return
	}
	a.win.OnClose(a.closeDesktopWindow)
	a.win.OnShow(a.stopWatchingDesktopTray)
	mygo.App.OnQuit(a.releaseDesktopBackground)
	if runtime.GOOS == "darwin" {
		mygo.App.OnActivate(func(visible bool) {
			if !visible || !a.win.IsVisible() || a.win.IsMinimized() {
				a.showDesktopWindow()
			}
		})
	}
	a.syncDesktopBackground()
}

func (a *App) syncDesktopBackground() {
	d := &a.desktop
	enabled := a.m.Settings.BackgroundEnabled()
	if d.enabled == enabled {
		return
	}
	d.enabled = enabled
	if !enabled {
		a.releaseDesktopBackground()
		a.setDesktopFallbackMenu(false)
		return
	}
	if runtime.GOOS == "darwin" {
		d.releaseRunning = mygo.Power.KeepRunning("Colaunch")
	}
	if runtime.GOOS != "linux" || linuxTrayHostAvailable(context.Background()) {
		a.ensureDesktopTray()
	}
	if d.tray == nil && runtime.GOOS != "darwin" {
		a.setDesktopFallbackMenu(true)
	}
}

func (a *App) ensureDesktopTray() {
	if a.desktop.tray != nil {
		return
	}
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "main",
		Icon:    desktopTrayIcon,
		ToolTip: "Colaunch",
		Menu:    a.desktopTrayMenu(),
	})
	if err != nil {
		log.Printf("desktop tray: %v", err)
		return
	}
	a.desktop.tray = tray
}

func (a *App) closeDesktopWindow(e *mygo.CloseEvent) {
	if a.quitting || !a.m.Settings.BackgroundEnabled() {
		return
	}
	a.syncDesktopBackground()
	e.PreventDefault()

	switch runtime.GOOS {
	case "darwin":
		// Clicking the Dock icon can reopen the window even without a tray.
		a.win.Hide()
	case "windows":
		a.ensureDesktopTray()
		if tray := a.desktop.tray; tray != nil && tray.Bounds().Width > 0 {
			a.setDesktopFallbackMenu(false)
			a.win.Hide()
		} else {
			a.minimizeDesktopWindow()
		}
	case "linux":
		if linuxTrayHostAvailable(context.Background()) {
			a.ensureDesktopTray()
			if a.desktop.tray != nil {
				a.setDesktopFallbackMenu(false)
				a.win.Hide()
				a.watchLinuxTrayHost()
				return
			}
		}
		a.minimizeDesktopWindow()
	}
}

func (a *App) showDesktopWindow() {
	if a.quitting {
		return
	}
	a.win.Show()
	if a.win.IsMinimized() {
		a.win.Restore()
	}
	a.win.Focus()
}

func (a *App) minimizeDesktopWindow() {
	a.setDesktopFallbackMenu(true)
	if !a.win.IsVisible() {
		a.win.ShowInactive()
	}
	a.win.Minimize()
}

func (a *App) desktopTrayMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: i18n.T("background.open"), Click: func(*mygo.MenuItem, *mygo.Window) { a.showDesktopWindow() }},
		desktopQuitItem(),
	})
}

func desktopQuitItem() *mygo.MenuItem {
	return &mygo.MenuItem{Label: i18n.T("background.quit"), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }}
}

func (a *App) setDesktopFallbackMenu(visible bool) {
	if a.desktop.fallbackMenu == visible {
		return
	}
	a.desktop.fallbackMenu = visible
	if visible {
		a.refreshDesktopFallbackMenu()
	} else {
		a.win.SetMenu(nil)
	}
}

func (a *App) refreshDesktopFallbackMenu() {
	a.win.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Colaunch", Submenu: []*mygo.MenuItem{desktopQuitItem()}},
	}))
}

func (a *App) refreshDesktopBackgroundMenus() {
	if a.quitting {
		return
	}
	if tray := a.desktop.tray; tray != nil {
		tray.SetMenu(a.desktopTrayMenu())
	}
	if a.desktop.fallbackMenu {
		a.refreshDesktopFallbackMenu()
	}
}

func (a *App) releaseDesktopBackground() {
	a.stopWatchingDesktopTray()
	d := &a.desktop
	if d.tray != nil {
		d.tray.Destroy()
		d.tray = nil
	}
	if d.releaseRunning != nil {
		d.releaseRunning()
		d.releaseRunning = nil
	}
}

// AppIndicator can create an icon even on a desktop with no panel that
// displays it. Require a registered host before hiding the only window.
func linuxTrayHostAvailable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gdbus", "call", "--session",
		"--dest", "org.kde.StatusNotifierWatcher",
		"--object-path", "/StatusNotifierWatcher",
		"--method", "org.freedesktop.DBus.Properties.Get",
		"org.kde.StatusNotifierWatcher", "IsStatusNotifierHostRegistered").Output()
	return err == nil && strings.TrimSpace(string(out)) == "(<true>,)"
}

// A Linux panel or its tray extension can disappear while Colaunch is hidden.
// Put the window back in the taskbar if the host is lost.
func (a *App) watchLinuxTrayHost() {
	if a.desktop.stopWatching != nil {
		return
	}
	ctx, stop := context.WithCancel(context.Background())
	a.desktop.stopWatching = stop
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if linuxTrayHostAvailable(ctx) {
					continue
				}
				mygo.RunOnMain(func() {
					if ctx.Err() == nil && !a.quitting {
						a.stopWatchingDesktopTray()
						a.minimizeDesktopWindow()
					}
				})
				return
			}
		}
	}()
}

func (a *App) stopWatchingDesktopTray() {
	if stop := a.desktop.stopWatching; stop != nil {
		stop()
		a.desktop.stopWatching = nil
	}
}
