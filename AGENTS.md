# Repository Guidelines

## Project Structure & Module Organization

The module is `github.com/rusq/kilo`, a Go terminal text editor. The root package contains editing and syntax highlighting (`kilo.go`), Unicode positioning (`positions.go`), text projection (`text.go`), and terminal/input handling (`terminal*.go`, `rawinput.go`, `wait_*.go`). Platform-specific files cover macOS, Linux, and Windows. `cmd/kilo/main.go` provides the CLI. Tests live beside source files as `*_test.go`. `csrc/` contains the original C implementation and attribution for reference.

Read `SPEC.md` for behavior contracts, but verify paths against the current layout: its references to an `internal/` package reflect an earlier location.

## Build, Test, and Development Commands

Use the Go version declared in `go.mod` (currently 1.27.2). Run commands from the repository root:

- `go build ./...` — compile all packages.
- `go run ./cmd/kilo /tmp/example.txt` — open a file in an interactive terminal; Ctrl-S saves and Ctrl-Q quits.
- `go test ./...` — run the complete test suite.
- `go test -race ./...` — check concurrent input and shutdown behavior for data races.
- `go test -run TestSyntaxHighlighting ./...` — run a focused test.
- `go vet ./...` — run Go's static checks.

## Coding Style & Naming Conventions

Format changed Go files with `gofmt -w`; use its tab indentation and standard import grouping. Use exported CamelCase names for public APIs and lower camelCase for private helpers. Preserve OS filename suffixes and build constraints. Keep terminal rendering behind `tcell` and use `uniseg` for grapheme boundaries. Document exported behavior, especially descriptor ownership and error recovery.

## Testing Guidelines

Tests use Go's `testing` package, table-driven subtests, `t.TempDir()`, and `tcell.NewSimulationScreen`. Name tests `TestBehavior` and add regression cases beside the affected subsystem. Cover byte-preserving saves, Unicode cursor movement, paste framing, cancellation, and terminal cleanup when relevant. No coverage percentage is configured. Verify platform-specific terminal changes on the affected operating system.

## Commit & Pull Request Guidelines

There is no commit history yet, so no established message convention exists. Use concise, imperative subjects such as `Fix cursor movement across graphemes`. Keep changes focused. PR descriptions should explain the behavior change, link related issues when applicable, and list validation commands and results. Include terminal captures for visible UI changes and identify platform limitations. Preserve license attribution.
