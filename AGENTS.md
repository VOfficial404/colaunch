# AGENTS.md

Colaunch is a Go app with MyGo's native UI (github.com/egoist/mygo). Read docs/architecture.md first.

## Rules

- `internal/core` holds state and talks to Colab; `internal/app` only shows it. The view runs on the main thread; goroutines change state through `Manager.Post`, never directly.
- Every user-visible string goes through `i18n.T`/`i18n.F` with entries in all three languages (`internal/i18n/strings.go`). `go test ./internal/i18n` checks it.
- The runtime side (`agent.py`, `llama.sh`) uses only Python's standard library and bash, and must keep working with the app closed.
- Never commit OAuth clients, tokens or keys. Builds get the OAuth client through `MYGO_LDFLAGS`.

## Tests

Only test behaviour that can break: parsing, choosing quantizations, the tunnel (it runs the real agent.py), the limits on cost, and what clicks do in views (`ui.NewTester`, `newTestApp` in `internal/app/app_test.go`). Don't test that labels show or MyGo itself. Run `go vet ./... && go test ./...` before committing.
