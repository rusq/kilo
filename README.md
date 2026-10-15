# gokilo

A small terminal text editor written in Go, based on Salvatore Sanfilippo's
[Kilo](https://github.com/antirez/kilo). Use it as a command-line editor or embed
the `kilo` package in a Go application.

The editor uses [tcell](https://github.com/gdamore/tcell) for terminal rendering
and includes:

- Unicode editing with grapheme-aware cursor movement and deletion.
- Syntax highlighting for C, C++, and SQL.
- Incremental search with wraparound navigation.
- Bracketed paste, scrolling, and terminal resize handling.
- Temporary-file saves and recovery of unsaved text on cancellation or failure.

Terminal implementations are provided for macOS, Linux, and Windows.

## Getting started

Use Go 1.27.2 or newer, as required by [go.mod](go.mod), and an interactive
terminal. From the repository root, open an existing file or start a new one:

```sh
go run ./cmd/kilo notes.txt
```

Press **Ctrl-S** to save and **Ctrl-Q** to quit. A new file is created when you
save it; its parent directory must already exist.

To install the command from this checkout:

```sh
go install ./cmd/kilo
kilo notes.txt
```

The installed executable is named `kilo`. Ensure your Go binary installation
directory (`GOBIN`, or usually `$(go env GOPATH)/bin`) is on `PATH`.

## Key bindings

| Key | Action |
| --- | --- |
| Arrow keys | Move the cursor |
| Home / End | Move to the start / end of the line |
| Page Up / Page Down | Move one screen up / down |
| Enter / Tab | Insert a newline / tab |
| Backspace / Delete / Ctrl-H | Delete before the cursor |
| Ctrl-S | Save |
| Ctrl-F | Search |
| Ctrl-Q / Ctrl-X | Quit, with confirmation if there are unsaved changes |
| Ctrl-C | Cancel editing; attempt to recover unsaved text |

With unsaved changes, the first quit key displays a warning; press it three more
times consecutively to discard changes and exit. Other editing keys reset the
confirmation counter.

In search mode, type a query, use Right/Down for the next match or Left/Up for
the previous match, then press Enter to keep the position. Escape cancels the
search and restores the original cursor position.

## Files, paste, and recovery

Files are saved with LF line endings; CRLF input is normalized on load. The
presence or absence of a final newline is preserved when opening and saving
without edits. Control characters and bidirectional formatting controls are
projected visibly without changing the stored source merely for display.

Bracketed paste treats pasted content as text rather than editor commands. Raw
paste capture has a 1 MiB limit per paste, independent of document size.

Cancellation or another editing failure with unsaved changes attempts to write
a recovery file in a private `kilo-recovery-*` directory under the system's
temporary directory. The returned error, also printed by the CLI, includes its
path. Recovery files remain available for the user or calling application to
inspect and remove. A normal confirmed discard exits without recovery; a prior
failed save still triggers recovery of the unsaved buffer.

## Use as a Go library

Import `github.com/rusq/kilo` and call `Run` or `RunContext`:

```go
package main

import (
	"context"
	"log"

	"github.com/rusq/kilo"
)

func main() {
	if err := kilo.RunContext(context.Background(), "notes.txt"); err != nil {
		log.Fatal(err)
	}
}
```

`Run(filename)` uses a background context. Both functions open their own
terminal descriptors and block until editing ends. To supply terminal
descriptors, use `RunTerminal(ctx, filename, input, output)`; it borrows the
`*os.File` handles and leaves them open. Terminal state is restored on exit.

Use `errors.Is` to detect `context.Canceled` or `kilo.ErrPasteTooLarge`, and
`errors.As` to inspect `*kilo.RecoveryError`. Its `Path` identifies recovered
text, and `Partial` indicates incomplete captured paste input.

## Development

```sh
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

Tests use Go's testing package and tcell simulation screens. The root package
contains the editor and terminal implementations; `cmd/kilo/` contains the CLI,
and `csrc/` preserves the original C implementation. See
[AGENTS.md](AGENTS.md) for contributor guidelines and [SPEC.md](SPEC.md) for
design details. Some specification sections retain descriptions of earlier
behavior; consult the implementation and tests when making changes.

## License

Distributed under the [BSD 2-Clause license](LICENSE), with attribution to
Salvatore Sanfilippo for the original Kilo editor and Rustam Gilyazov for the Go
reimplementation. Original C attribution is also retained in
[csrc/LICENSE](csrc/LICENSE).
