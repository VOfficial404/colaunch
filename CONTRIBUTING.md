# Contributing

Thanks for helping! Issues and pull requests are welcome, in English or Chinese.

## Setting up

Install Go 1.27 or later, then:

```sh
go tool mygo dev      # runs the app, rebuilding as you edit
go test ./...         # set COLAUNCH_TEST_PYTHON on Windows for the tunnel's tests
go vet ./...
```

`COLAUNCH_SHOTS=/some/dir go test ./internal/app -run TestPagesBuild` writes
a screenshot of every page, at a desktop's size and a phone's, light and
dark, to look at the interface without a Colab account.

## Guidelines

- Keep the runtime's side (`internal/agent/agent.py`, `internal/plugins/*.sh`) to Python's standard library and bash: Colab's images change.
- Strings the interface shows go in `internal/i18n/strings.go`, in all three languages; a test fails when one is missing.
- State the view reads changes on the main thread only: from goroutines, through `Manager.Post`.
- Test what can break: logic without a view, and what input does. See AGENTS.md.
- Note user-facing changes in CHANGELOG.md.

## Releasing

Bump `version` in `mygo.json`, add its section to CHANGELOG.md, then push a tag `v<version>`. The release workflow builds every platform.
