# Kilo Editor – Go Implementation Spec

## Overview

Kilo is a minimal terminal text editor. The Go implementation is a pure-Go
reimplementation of the original C version (Salvatore Sanfilippo, 2016),
using `github.com/gdamore/tcell/v2` for all terminal interaction instead of
raw VT100 escape sequences.

Public entry points:

```go
func Run(filename string) error
func RunContext(ctx context.Context, filename string) error
func RunTerminal(ctx context.Context, filename string, input, output *os.File) error
```

Blocks until the user quits. Returns nil on clean exit, error on I/O or
terminal-init failure.

`Run` delegates to `RunContext` with a background context. `RunContext` opens
and closes its own terminal descriptors. `RunTerminal` borrows the caller's
descriptors, restores terminal state on exit, and leaves the descriptors open.
Cancellation remains discoverable through `errors.Is`.

## Package boundary and portability

Kilo's source and tests depend only on the standard library and external
modules (`tcell/v2`, `uniseg`, `x/sys`, and `x/term`); they do not import other
todo packages. Copy the package, including platform files, tests and attribution,
into another module to reuse it. Its current location under `internal/` prevents
direct imports from unrelated projects; publishing an importable package or
separate module is a separate distribution decision. The supported terminal
platforms are macOS, Linux and Windows.

Terminal-safe text projection belongs to Kilo and affects display and interactive
input filtering, never the stored source. Application-specific document
validation belongs to the caller after editing.

Raw bracketed-paste capture has a private 1 MiB payload limit, independent of file
or total document size. Delimiter bytes do not count toward that limit. Oversized
paste returns `ErrPasteTooLarge`, discoverable through `errors.Is`, and retains
the first limit-plus-one source bytes as partial input for recovery. Incomplete
delimiter prefixes may temporarily be retained while framing is unresolved.

`RecoveryError` exposes `Path`, `Partial`, and the underlying `Err` through
`Unwrap`. Dirty buffers on failure are retained in a private `kilo-recovery-*`
directory (0700) with a 0600 source file; `Partial` identifies incomplete captured
input. The caller owns recovery-file cleanup. Todo's `noteedit` adapter maps paste
overflow to its invalid-input class and cancellation to its interrupted class,
preserving recovery details. Kilo has no knowledge of those application classes.

---

## Data model

### `erow` – one line of the file

| Field    | Type       | Meaning                                      |
|----------|------------|----------------------------------------------|
| `idx`    | `int`      | Zero-based row index in `editor.rows`        |
| `chars`  | `[]byte`   | Raw file content (no newline)                |
| `render` | `[]byte`   | Display content: tabs expanded to 8-col stops|
| `hl`     | `[]hlType` | Highlight type per `render` byte             |
| `hlOC`   | `bool`     | Row ends inside an open multi-line comment   |

### `editor` – global state

| Field           | Meaning                                      |
|-----------------|----------------------------------------------|
| `cx`, `cy`      | Cursor position within the visible window    |
| `rowOff`        | First visible file row (vertical scroll)     |
| `colOff`        | First visible file column (horizontal scroll)|
| `screenRows`    | Terminal height − 2 (status + message bars)  |
| `screenCols`    | Terminal width                               |
| `rows`          | `[]*erow` – all lines                        |
| `dirty`         | Unsaved changes flag                         |
| `filename`      | Path of the open file                        |
| `statusMsg`     | Current status-bar message text              |
| `statusMsgTime` | Time the message was set (expires after 5 s) |
| `syntax`        | Active `*editorSyntax`, or nil               |
| `screen`        | `tcell.Screen`                               |
| `quitTimes`     | Decremented on each quit-while-dirty attempt |

---

## Syntax highlighting

### Highlight types (`hlType`)

| Constant      | Colour (tcell)       | Meaning                       |
|---------------|----------------------|-------------------------------|
| `hlNormal`    | default              | Plain text                    |
| `hlNonprint`  | reverse              | Non-printable character       |
| `hlComment`   | Teal                 | Single-line comment           |
| `hlMLComment` | Teal                 | Multi-line comment body       |
| `hlKeyword1`  | Yellow               | Primary keyword               |
| `hlKeyword2`  | Green                | Secondary keyword (types)     |
| `hlString`    | Purple               | String literal                |
| `hlNumber`    | Red                  | Numeric literal               |
| `hlMatch`     | Cornflower blue      | Active search match           |

### `editorSyntax` struct

```
fileMatch         []string   // extension or substring patterns
keywords          []string   // trailing "|" marks keyword2
singleLineComment string     // e.g. "//" or "--"
mlCommentStart    string     // e.g. "/*"
mlCommentEnd      string     // e.g. "*/"
flags             int        // flagHighlightStrings | flagHighlightNumbers
```

### Built-in syntax entries

| Entry | File patterns              | Comment tokens | Flags              |
|-------|----------------------------|----------------|--------------------|
| C/C++ | `.c .h .cpp .hpp .cc`      | `//` `/* */`   | strings + numbers  |
| SQL   | `.sql .sqlx`               | `--` `/* */`   | strings + numbers  |

SQL keywords are matched **case-insensitively**.

### `updateSyntax` algorithm (per row)

1. Inherit open-comment state from the previous row's `hlOC`.
2. Scan `render` left-to-right:
   - Single-line comment start → mark rest of row `hlComment`, stop.
   - Inside multi-line comment → mark `hlMLComment`; exit on `mlCommentEnd`.
   - Multi-line comment start → mark two bytes `hlMLComment`, enter comment.
   - Inside string → mark `hlString`; handle `\"` escape; exit on matching quote.
   - String delimiter (`"` or `'`) → enter string.
   - Non-printable (`< 32` or `== 127`) → `hlNonprint`.
   - Digit after separator, or `.` after `hlNumber` → `hlNumber`.
   - Word after separator matching a keyword → `hlKeyword1` or `hlKeyword2`.
3. After scan, if `hlOC` changed, **recursively** call `updateSyntax` on
   the next row (propagates multi-line comment state through the file).

A **separator** is: NUL, whitespace, or any of `` , . ( ) + - / * = ~ % [ ] ; ``.

---

## Tab rendering

Tabs are expanded in `updateRow` to 8-column stops:

```
append ' '
while len(render) % 8 != 0 { append ' ' }
```

---

## Key bindings

| Key(s)                           | Action                                    |
|----------------------------------|-------------------------------------------|
| Arrow keys                       | Move cursor                               |
| Page Up / Page Down              | Scroll one screen                         |
| Home                             | Move to column 0                          |
| End                              | Move to end of line                       |
| Enter                            | Insert newline (split line)               |
| Backspace / Ctrl-H / Delete      | Delete character before/at cursor         |
| Ctrl-S                           | Save file                                 |
| Ctrl-F                           | Enter find mode                           |
| Ctrl-Q / Ctrl-X                  | Quit (3 presses required if dirty)        |
| Ctrl-C                           | Ignored (prevents accidental data loss)   |
| Ctrl-L / Escape                  | No-op (triggers screen refresh)           |
| Printable ASCII                  | Insert character                          |

### Find mode

Entered with Ctrl-F. Incremental forward search.

| Key                   | Action                     |
|-----------------------|----------------------------|
| Printable char        | Append to query            |
| Backspace / Delete    | Remove last query character|
| Arrow Right / Down    | Jump to next match         |
| Arrow Left / Up       | Jump to previous match     |
| Enter                 | Confirm, keep cursor       |
| Escape                | Cancel, restore cursor     |

Matches are highlighted with `hlMatch` while searching; highlight is
restored on exit. Search wraps around end/start of file.

---

## Rendering

`refreshScreen` is called before every `PollEvent`. It:

1. Queries terminal size and updates `screenRows`/`screenCols`.
2. Clears the screen (`screen.Clear()`).
3. Draws content rows:
   - Rows beyond EOF show `~` (or a centered welcome message when the
     buffer is empty, at one-third of the screen height).
   - Each visible character is placed with `screen.SetContent(x, y, r, nil, style)`.
   - Non-printable bytes are shown as `@` + control-code offset in reverse video.
4. Draws the **status bar** at `screenRows` in reverse video:
   `<filename> - <N> lines [(modified)]` left-aligned,
   `<current>/<total>` right-aligned.
5. Draws the **message bar** at `screenRows+1`: displays `statusMsg` for
   up to 5 seconds after it was set.
6. Positions the terminal cursor with `screen.ShowCursor(cx, cy)`, where
   `cx` accounts for tab expansion between `colOff` and the logical cursor.
7. Flushes with `screen.Show()`.

---

## File I/O

### Open

`openFile` reads the file line by line with `bufio.Scanner`, calling
`insertRow` for each line. If the file does not exist the editor starts
with an empty buffer (no error). After loading, `dirty` is reset to false.

### Save

`saveFile` serialises all rows via `rowsToString` (each row joined with
`\n`) and writes atomically with `os.WriteFile` (mode `0644`). On success
`dirty` is cleared and the byte count is shown in the message bar.

---

## Cursor movement details

`moveCursor(key)` handles the four arrow keys:

- **Left**: decrement `cx`; if already at column 0 and `colOff > 0`
  scroll left; if at the very start of a line, jump to the end of the
  previous line (adjusting `colOff` if needed).
- **Right**: increment `cx` or `colOff`; at end-of-line, move to the
  first column of the next line.
- **Up/Down**: decrement/increment `cy` or `rowOff`.

After every move, `cx` is clamped to the length of the new row.

Page Up/Down: snap `cy` to top/bottom of the visible window, then issue
`screenRows` individual Up/Down moves.

---

## Startup sequence (`Run`)

1. Create and initialise `tcell.Screen`.
2. Set `editor.filename`, call `selectSyntaxHighlight`.
3. Call `openFile` (reads file; or creates empty buffer).
4. Set initial status message with key-binding help.
5. Enter event loop:
   - `refreshScreen()`
   - `screen.PollEvent()` → handle `EventResize` (sync) or `EventKey`
     (processKey → return false to exit).
6. `screen.Fini()` on exit (deferred).

## Note integration amendments (2026-10-14)

Run(filename) retains its entry point and delegates to RunContext(ctx,filename).
Context cancellation and Ctrl-C cancel; failed saves/cancellation with a dirty
buffer return RecoveryError with private recovered source, without importing
unsaved text. Explicit quit after the dirty confirmation discards unsaved buffers
unless a save failed, in which case recovery is retained. Terminal finalisation
runs after init failure as well as all ordinary/error exits.

Cursor offsets are UTF-8 grapheme boundaries in bytes; projection and horizontal
scrolling use terminal cells, including tabs/wide/combining/ZWJ characters. Find
input and deletion also respect Unicode. Existing source spelling and exact final
newline state survive saves. Backspace at the virtual EOF removes the final LF,
allowing an editor buffer to be cleared completely.

Save uses a private temporary file, flush/sync/close and rename, with cleanup on
failure; it preserves the previous saved file rather than truncating it first.
Files and recovery source use 0600 permissions; recovery directories use 0700 on
platforms with POSIX modes. Notes are validated by the shared workflow after the
editor exits. This amendment supersedes the earlier byte-based editing/rendering,
Ctrl-C-ignore, always-final-newline and os.WriteFile atomicity descriptions.

Bracketed paste inserts source in a batch, without interpreting pasted control
keys as editor commands. Interrupted paste retains the captured prefix in the
dirty-buffer recovery. End movement and horizontal scrolling scan the row
linearly. Unsafe/nonprinting source is projected as visible escapes (or a dotted
circle for a standalone mark); saved bytes remain unchanged.
Find also retains paste boundaries, so multiline/control clipboard input cannot
leave search and execute editing commands. Its nested loop checks cancellation
and terminal EOF directly; wakeup retries tolerate a full screen event queue.
Typing/pasting into the virtual EOF preserves the former final LF as a row
separator rather than appending an additional LF after the new text.
Horizontal viewport origin is one cell offset shared by every displayed row;
each row clips its own graphemes against that origin. Editing offsets remain byte
boundaries in the active row.
Insertion advances the caret past the complete resulting grapheme when inserted
text joins following source. Clipboard CR is retained during collection, then
CRLF/lone CR normalizes once to LF, including interrupted-paste recovery. An
unfinished captured paste is identified as partial recovery input.

Raw clipboard framing precedes key decoding and retains incomplete multi-byte
paste headers across idle/read boundaries. A lone Escape emits once after its
timeout while retaining a sentinel for late CSI/paste continuation, matching
the TUI fragmentation policy. An already delivered Escape may leave Find before
a late header is recognized, but clipboard bytes still remain literal source
and cannot become action keys. Repeated Escape preserves the following frame.
