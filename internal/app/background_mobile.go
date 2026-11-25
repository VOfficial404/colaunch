package app

import (
	"context"
	"log"
	"runtime"
	"time"

	"github.com/egoist/mygo"

	"colaunch/internal/i18n"
	"colaunch/internal/mobilebg"
)

// mobileBackground is owned by the main thread. Keep native calls in order
// when the user toggles the setting or the last task ends during a start.
type mobileBackground struct {
	ready       bool
	foreground  bool
	inFlight    bool
	resumed     bool
	lastRequest backgroundRequest
}

type backgroundRequest struct {
	active bool
	body   string
}

func (a *App) initMobileBackground() {
	if runtime.GOOS != "android" && runtime.GOOS != "ios" {
		return
	}
	a.mobile.ready = true
	a.mobile.foreground = true
	mygo.Power.OnSuspend(func() {
		a.mobile.foreground = false
		a.setCoreBackground(true)
	})
	mygo.Power.OnResume(func() {
		a.mobile.foreground = true
		a.setCoreBackground(false)
		// Android can stop the service while the app is in the background.
		// Retry once the user has brought the app to the foreground again.
		a.mobile.resumed = true
		a.syncMobileBackground()
		a.m.RefreshConnections()
	})
	a.syncMobileBackground()
}

// setCoreBackground tells the manager whether the app is in the
// background, for it to do less there.
func (a *App) setCoreBackground(on bool) { a.m.SetBackground(on) }

func (a *App) mobileBackgroundRequest() backgroundRequest {
	if a.m.Settings.BackgroundEnabled() {
		for _, s := range a.m.Sessions {
			if !s.Ended() {
				return backgroundRequest{active: true, body: i18n.T("background.notification")}
			}
		}
	}
	return backgroundRequest{}
}

func (a *App) syncMobileBackground() {
	b := &a.mobile
	if !b.ready || b.inFlight || a.quitting {
		return
	}
	request := a.mobileBackgroundRequest()
	resumed := b.resumed
	b.resumed = false
	if request.active && !b.foreground {
		return
	}
	if request == b.lastRequest && !(request.active && resumed) {
		return
	}
	b.lastRequest, b.inFlight = request, true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := mobilebg.Set(ctx, request.active, "Colaunch", request.body)
		mygo.RunOnMain(func() {
			b.inFlight = false
			if a.quitting {
				return
			}
			if err != nil {
				log.Printf("mobile background: %v", err)
				if b.foreground && a.mobileBackgroundRequest() == request {
					a.notify("error", i18n.T("background.unavailable"), "")
					a.win.Invalidate()
				}
			}
			a.syncMobileBackground()
		})
	}()
}
