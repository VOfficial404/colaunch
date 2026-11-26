package app

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"colaunch/internal/i18n"
	"colaunch/internal/update"
)

// How often the app checks by itself, and how soon after launching.
const (
	updateInterval = 12 * time.Hour
	updateDelay    = 10 * time.Second
	// updateRetry is the wait after an automatic check failed.
	updateRetry = time.Hour
)

var iconUpdate = lucide(`<circle cx="12" cy="12" r="10"/><path d="m16 12-4-4-4 4"/><path d="M12 16V8"/>`)

// updateState is what the app knows of updates; the main thread owns it.
type updateState struct {
	checker *update.Checker
	// info is what the last check found, when has.
	info update.Info
	has  bool
	// skipped is the version the user skipped.
	skipped  string
	checking bool
	// manual is set while a check the user asked for runs: its result
	// shows in a toast.
	manual bool
	// open shows the update dialog.
	open  bool
	timer *time.Timer
	// goos is the platform the dialog offers downloads for.
	goos string
	// async runs a check; on a goroutine but in tests.
	async func(func())
	md    markdown.Options
}

// mygoUpdater is MyGo's updater, which installs updates on desktops.
type mygoUpdater struct{}

func (mygoUpdater) Enabled() bool { return mygo.Updater.Enabled() }

func (mygoUpdater) Check(ctx context.Context) (string, string, error) {
	up, err := mygo.Updater.Check(ctx)
	if err != nil || up == nil {
		return "", "", err
	}
	return up.Version, up.Notes, nil
}

// updatePlugin is MyGo's update window, in native UI: it installs what
// the app's own checks find, so its own checks stay off.
func updatePlugin() mygo.Plugin {
	return native.New(updater.Options{DisableAutomaticChecks: true})
}

// initUpdates loads what the last run learned and starts checking.
func (a *App) initUpdates(dir string) {
	u := &a.update
	cfg := update.Config{
		Current: appVersion(),
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Path:    filepath.Join(dir, "update.json"),
	}
	if isDesktop() {
		cfg.Native = mygoUpdater{}
	}
	u.checker = update.New(cfg)
	u.info, u.has = u.checker.Last()
	u.skipped = u.checker.Skipped()
	a.scheduleUpdates(updateDelay)
	mygo.App.OnQuit(func() { u.timer.Stop() })
	// Back in the foreground after a long while, as a phone's app usually
	// is, it checks again.
	back := func() { a.m.Post(a.resumeUpdates) }
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		mygo.Power.OnResume(back)
	} else if a.win != nil {
		a.win.OnFocus(back)
	}
}

// scheduleUpdates arms the next automatic check, d from now.
func (a *App) scheduleUpdates(d time.Duration) {
	u := &a.update
	if u.timer != nil {
		u.timer.Stop()
	}
	u.timer = time.AfterFunc(d, func() { a.m.Post(a.autoCheckUpdates) })
}

func (a *App) autoCheckUpdates() {
	if !a.m.Settings.AutoUpdateChecks() || a.quitting {
		a.scheduleUpdates(updateInterval)
		return
	}
	a.checkUpdates(false)
}

func (a *App) resumeUpdates() {
	u := &a.update
	if u.checker == nil || u.checking || !a.m.Settings.AutoUpdateChecks() {
		return
	}
	if time.Since(u.checker.CheckedAt()) >= updateInterval {
		a.checkUpdates(false)
	}
}

// checkUpdates checks for updates; manual ones say what they found.
func (a *App) checkUpdates(manual bool) {
	u := &a.update
	if manual {
		u.manual = true
	}
	if u.checker == nil || u.checking {
		return
	}
	u.checking = true
	chk := u.checker
	run := u.async
	if run == nil {
		run = func(fn func()) { go fn() }
	}
	run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		info, err := chk.Check(ctx)
		a.m.Post(func() { a.updateResult(info, err) })
	})
}

// updateResult takes a check's result, on the main thread.
func (a *App) updateResult(info update.Info, err error) {
	u := &a.update
	manual := u.manual
	u.checking, u.manual = false, false
	next := updateInterval
	if err != nil {
		next = updateRetry
		var rl *update.RateLimitError
		if errors.As(err, &rl) && time.Until(rl.Until) > next {
			next = time.Until(rl.Until)
		}
	} else {
		u.info, u.has = info, true
	}
	if u.checker != nil {
		a.scheduleUpdates(next)
	}
	if !manual {
		// Automatic checks fail quietly: offline, or GitHub busy.
		return
	}
	switch {
	case err != nil:
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("update.failed"), Description: updateError(err), Type: "error"})
	case info.Available:
		a.toasts = append(a.toasts, ui.Toast{
			Title: i18n.F("update.found", info.Latest), Type: "info", Timeout: 8e9,
			Action: i18n.T("update.show"), OnAction: func() { a.update.open = true },
		})
	case !isVersion(info.Current):
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("update.dev_build"), Description: i18n.F("update.version", info.Latest), Type: "info"})
	default:
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("update.up_to_date"), Description: i18n.F("update.up_to_date_v", info.Current), Type: "success", Timeout: 3e9})
	}
}

// updateError says why a check failed.
func updateError(err error) string {
	var rl *update.RateLimitError
	var ne net.Error
	var se *update.StatusError
	switch {
	case errors.As(err, &se):
		return i18n.F("update.http_status", se.Code)
	case errors.As(err, &rl):
		return i18n.F("update.rate_limited", rl.Until.Local().Format("15:04"))
	case errors.Is(err, update.ErrNoRelease):
		return i18n.T("update.no_release")
	case errors.As(err, &ne):
		return i18n.T("update.offline")
	}
	return err.Error()
}

// updateOffered reports whether there is an update to tell the user of:
// newer, and not skipped.
func (a *App) updateOffered() bool {
	u := &a.update
	return u.has && u.info.Available && u.info.Latest != u.skipped
}

// skipUpdate skips the version offered until the next.
func (a *App) skipUpdate() {
	u := &a.update
	u.skipped = u.info.Latest
	if u.checker != nil {
		u.checker.Skip(u.skipped)
	}
	u.open = false
	a.toasts = append(a.toasts, ui.Toast{Title: i18n.F("update.skipped", u.skipped), Type: "info", Timeout: 4e9})
}

func (a *App) updatePlatform() string {
	if a.update.goos != "" {
		return a.update.goos
	}
	return runtime.GOOS
}

// updateNotice is the sidebar's line above the account, while an update
// is offered.
func (a *App) updateNotice(c *ui.Context) {
	t := c.Theme()
	text := i18n.F("update.available", a.update.info.Latest)
	row := ui.Row(c.Key("update-notice")).Gap(8).AlignItems(ui.Center).Margin(0, 10, 8, 10).Padding(8, 10).Radius(8).
		Background(t.Accent.Alpha(0.12)).Focusable().Role(ui.RoleButton).Label(i18n.F("update.available_label", a.update.info.Latest))
	row.Children(func() {
		ui.Icon(c, iconUpdate).TextColor(t.Accent)
		ui.Text(c, text).FontSize(12).Bold().TextColor(t.Accent).Grow(1).Shrink(1)
	})
	if row.Clicked() {
		a.update.open = true
	}
}

// updateCard is the card atop phones' Settings while an update is offered.
func (a *App) updateCard(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("update-card")).Padding(14, 16).Gap(10).Radius(12).Background(t.Accent.Alpha(0.10)).Border(1, t.Accent.Alpha(0.5)).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconUpdate).FontSize(20).TextColor(t.Accent)
			ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
				ui.Text(c, i18n.T("update.card_title")).Bold()
				ui.Text(c, i18n.F("update.available", a.update.info.Latest)).FontSize(13).TextColor(t.TextMuted)
			})
		})
		ui.Row(c).Children(func() {
			if ui.PrimaryButton(c, i18n.T("update.show")).Clicked() {
				a.update.open = true
			}
		})
	})
}

// settingsDot is the red dot on phones' Settings tab while an update is
// offered.
func settingsDot(c *ui.Context) {
	ui.Column(c.Key("update-dot")).Absolute().Top(0).Right(8).Width(8).Height(8).Radius(4).Background(c.Theme().Danger)
}

// updateAbout is Settings → About's part on updates.
func (a *App) updateAbout(c *ui.Context) {
	t := c.Theme()
	u := &a.update
	switch {
	case u.checking:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, i18n.T("update.checking")).FontSize(13).TextColor(t.TextMuted)
		})
	case u.has && !u.info.CheckedAt.IsZero():
		ui.Text(c, i18n.F("update.last_check", u.info.CheckedAt.Local().Format("2006-01-02 15:04"))).FontSize(13).TextColor(t.TextMuted)
	default:
		ui.Text(c, i18n.T("update.never_checked")).FontSize(13).TextColor(t.TextMuted)
	}
	if u.has && u.info.Available {
		ui.Row(c).Children(func() {
			if labeledButton(c, iconUpdate, i18n.F("update.available", u.info.Latest), false).TextColor(t.Accent).Clicked() {
				u.open = true
			}
		})
	}
	on := a.m.Settings.AutoUpdateChecks()
	if switchRow(c, &on, i18n.T("update.auto")) {
		a.m.Settings.AutoUpdateCheck = &on
		a.m.SaveSettings()
		if on {
			a.resumeUpdates()
		}
	}
	ui.Text(c, i18n.T("update.auto_help")).FontSize(12).TextColor(t.TextMuted)
}

// updateDialog shows the update offered: versions, notes and how to get
// it on this platform.
func (a *App) updateDialog(c *ui.Context) {
	u := &a.update
	if !u.open || !u.has {
		return
	}
	t := c.Theme()
	info := u.info
	w, h := c.Size()
	// The modal keeps 20 DIPs around its panel, which pads 20 inside.
	width := min(float32(560), w-84)
	notesHeight := max(float32(120), min(float32(360), h-360))
	ui.Modal(c, &u.open, func() {
		ui.Column(c.Key("update-dialog")).Width(width).Gap(14).Role(ui.RoleDialog).Label(i18n.T("update.title")).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconUpdate).FontSize(22).TextColor(t.Accent)
				ui.Text(c, i18n.T("update.title")).FontSize(18).Bold()
			})
			ui.Row(c).Gap(12).Wrap().Children(func() {
				stat(c, info.Current, i18n.T("update.current"))
				stat(c, info.Latest, i18n.T("update.latest"))
			})
			ui.Text(c, i18n.T("update.notes")).Bold().FontSize(14)
			ui.Scroll(c).FillWidth().MaxHeight(notesHeight).Radius(8).Border(1, t.Border).Children(func() {
				ui.Column(c).Padding(10, 12).Children(func() {
					if info.Notes == "" {
						ui.Text(c, i18n.T("update.no_notes")).FontSize(13).TextColor(t.TextMuted)
						return
					}
					markdown.View(c, info.Notes, &u.md)
				})
			})
			a.updateActions(c, info)
		})
	})
}

func (a *App) updateActions(c *ui.Context, info update.Info) {
	t := c.Theme()
	u := &a.update
	download := info.Download
	if download == "" {
		download = info.URL
	}
	var help string
	var primary func()
	label, icon := i18n.T("update.download"), iconDownload
	switch goos := a.updatePlatform(); {
	case goos == "android":
		help, label = "update.android_help", i18n.T("update.download_apk")
		primary = func() { openURL(download) }
	case goos == "ios":
		help, label, icon = "update.ios_help", i18n.T("update.release_page"), iconExternal
		primary = func() { openURL(info.URL) }
	case info.Native:
		help, label, icon = "update.install_help", i18n.T("update.install"), iconUpdate
		primary = func() {
			u.open = false
			// MyGo's window checks again, downloads, and offers to relaunch.
			updater.CheckForUpdates()
		}
	default:
		help = "update.manual_help"
		primary = func() { openURL(download) }
	}
	ui.Text(c, i18n.T(help)).FontSize(12).TextColor(t.TextMuted)
	ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		if labeledButton(c, icon, label, true).Clicked() {
			primary()
		}
		if a.updatePlatform() != "ios" && !info.Native && info.URL != "" && info.URL != download {
			if labeledButton(c, iconExternal, i18n.T("update.release_page"), false).Clicked() {
				openURL(info.URL)
			}
		}
		ui.Spacer(c)
		if ui.Button(c, i18n.T("update.skip")).Clicked() {
			a.skipUpdate()
		}
		if ui.Button(c, i18n.T("update.later")).Clicked() {
			u.open = false
		}
	})
}

// isVersion reports whether v is a release's version, not "dev".
func isVersion(v string) bool {
	_, ok := update.Compare(v, v)
	return ok
}
