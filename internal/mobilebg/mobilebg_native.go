//go:build android || ios

package mobilebg

import (
	"context"
	"errors"

	"github.com/egoist/mygo"
)

const pluginID = "colaunchMobileBackground"

// Set requests background time for ongoing work. Call it from a goroutine
// after MyGo is ready and while the app is visible for the initial start.
// Android uses a foreground service with the supplied notification text.
// iOS asks for a single, time-limited UIKit task. Call Set with active=false
// once the last operation ends.
//
// Canceling ctx stops the wait for MyGo's reply. It can leave the requested
// change in effect if it was already dispatched.
func Set(ctx context.Context, active bool, title, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !mygo.App.IsReady() {
		return errors.New("mobilebg: MyGo is not ready")
	}
	args := struct {
		Active bool   `json:"active"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}{active, title, body}
	return mygo.Native(pluginID).Call(ctx, "set", args, nil)
}
