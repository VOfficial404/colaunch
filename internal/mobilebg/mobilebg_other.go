//go:build !android && !ios

package mobilebg

import "context"

// Set is a no-op on non-mobile platforms.
func Set(_ context.Context, _ bool, _, _ string) error { return nil }
