// Package kilo is a pure Go text editor, reimplementing the Kilo editor.
package kilo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

// ── Syntax highlight types ────────────────────────────────────────────────────

type hlType uint8

const (
	hlNormal    hlType = iota
	hlNonprint         // non-printable character
	hlComment          // single-line comment
	hlMLComment        // multi-line comment
	hlKeyword1         // primary keyword
	hlKeyword2         // secondary keyword (types)
	hlString           // string literal
	hlNumber           // numeric literal
	hlMatch            // search match
)

const (
	flagHighlightStrings = 1 << 0
	flagHighlightNumbers = 1 << 1
)

type editorSyntax struct {
	fileMatch         []string
	keywords          []string
	keywordInfo       []syntaxKeyword
	singleLineComment string
	mlCommentStart    string
	mlCommentEnd      string
	flags             int
}

type syntaxKeyword struct {
	word   string
	length int
	hl     hlType
}

// ── Syntax database ──────────────────────────────────────────────────────────

var hlDB = []editorSyntax{
	{
		fileMatch: []string{".c", ".h", ".cpp", ".hpp", ".cc"},
		keywords: []string{
			// C keywords
			"auto", "break", "case", "continue", "default", "do", "else", "enum",
			"extern", "for", "goto", "if", "register", "return", "sizeof", "static",
			"struct", "switch", "typedef", "union", "volatile", "while", "NULL",
			// C++ keywords
			"alignas", "alignof", "and", "and_eq", "asm", "bitand", "bitor", "class",
			"compl", "constexpr", "const_cast", "decltype", "delete", "dynamic_cast",
			"explicit", "export", "false", "friend", "inline", "mutable", "namespace",
			"new", "noexcept", "not", "not_eq", "nullptr", "operator", "or", "or_eq",
			"private", "protected", "public", "reinterpret_cast", "static_assert",
			"static_cast", "template", "this", "thread_local", "throw", "true", "try",
			"typeid", "typename", "virtual", "xor", "xor_eq",
			// C types (trailing | marks keyword2)
			"int|", "long|", "double|", "float|", "char|", "unsigned|", "signed|",
			"void|", "short|", "auto|", "const|", "bool|",
		},
		singleLineComment: "//",
		mlCommentStart:    "/*",
		mlCommentEnd:      "*/",
		flags:             flagHighlightStrings | flagHighlightNumbers,
	},
	{
		fileMatch: []string{".sql", ".sqlx"},
		keywords: []string{
			"select", "insert", "update", "delete", "merge",
			"create", "alter", "truncate", "drop", "rename", "rebuild",
			"table", "index", "view", "materialized", "column", "cascade",
			"primary", "referencing", "key",
			"left", "right", "inner", "outer", "cartesian", "full", "cross",
			"from", "into", "join", "with", "as", "values", "exists", "in", "between",
			"limit", "offset", "order", "group", "over",
			"partition", "by", "cluster",
		},
		singleLineComment: "--",
		mlCommentStart:    "/*",
		mlCommentEnd:      "*/",
		flags:             flagHighlightStrings | flagHighlightNumbers,
	},
}

func init() {
	for i := range hlDB {
		hlDB[i].prepareKeywords()
	}
}

// ── Data types ───────────────────────────────────────────────────────────────

// erow represents one line of the file.
type erow struct {
	idx      int      // zero-based index in the file
	chars    []byte   // raw content
	render   []byte   // rendered content (tabs expanded)
	hl       []hlType // highlight type per render byte
	hlOC     bool     // open multi-line comment at end of row
	nonprint [][2]int // safely projected source spans, in render bytes
}

type editor struct {
	cx, cy           int // cursor position within the visible window
	rowOff           int // first visible row (scroll offset)
	colOff           int // first visible column (scroll offset)
	cellOff          int // shared horizontal viewport origin in terminal cells
	screenRows       int // usable rows (total height - 2 for status bars)
	screenCols       int // usable columns
	rows             []*erow
	dirty            bool
	filename         string
	statusMsg        string
	statusMsgTime    time.Time
	syntax           *editorSyntax
	screen           tcell.Screen
	quitTimes        int // decremented on each quit attempt while dirty
	ctx              context.Context
	omitFinalNewline bool
	finding          bool
	saveErr, exitErr error
}

const kiloVersion = "0.0.1"
const kiloQuitTimes = 3

// Run opens filename in the kilo editor and blocks until the user quits.
func Run(filename string) error { return RunContext(context.Background(), filename) }

// RunContext restores the terminal on every exit and retains unsaved text on
// cancellation or save failure. A clean explicit discard imports only saved bytes.
func RunContext(ctx context.Context, filename string) error {
	if filename == "" {
		return errors.New("filename not provided")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	input, output, err := openEditorTerminal()
	if err != nil {
		return fmt.Errorf("opening editor terminal: %w", err)
	}
	defer input.Close()
	if output != input {
		defer output.Close()
	}
	return RunTerminal(ctx, filename, input, output)
}

type RecoveryError struct {
	Path    string
	Partial bool
	Err     error
}

func (e *RecoveryError) Error() string {
	return fmt.Sprintf("%v; unsaved text recovered at %s", e.Err, e.Path)
}
func (e *RecoveryError) Unwrap() error { return e.Err }

func runScreen(ctx context.Context, filename string, screen tcell.Screen) (err error) {
	defer func() { err = errors.Join(err, finalizeScreen(screen)) }()
	if err = screen.Init(); err != nil {
		return fmt.Errorf("initialising screen: %w", err)
	}
	screen.Clear()
	screen.EnablePaste()
	width, height := screen.Size()
	e := &editor{screen: screen, screenCols: max(width, 1), screenRows: max(height-2, 1), quitTimes: kiloQuitTimes, ctx: ctx, omitFinalNewline: true}
	e.filename = filename
	e.selectSyntaxHighlight()
	if err = e.openFile(filename); err != nil {
		return err
	}
	var pasting bool
	var paste strings.Builder
	defer func() {
		if fault := recover(); fault != nil {
			err = fmt.Errorf("editor failed: %v", fault)
		}
		if raw, ok := screen.(interface{ stopPasteInput() ([]byte, bool) }); ok {
			captured, partial := raw.stopPasteInput()
			if err != nil && !e.finding && captured != nil {
				e.insertPaste(string(captured))
				pasting = partial
			}
		}
		if err != nil && pasting && paste.Len() > 0 {
			e.insertPaste(paste.String())
		}
		if err != nil && e.dirty {
			dir, recoveryErr := os.MkdirTemp("", "kilo-recovery-")
			if recoveryErr != nil {
				err = errors.Join(err, recoveryErr)
				return
			}
			path := filepath.Join(dir, "note.md")
			if recoveryErr = os.WriteFile(path, []byte(e.rowsToString()), 0600); recoveryErr != nil {
				err = errors.Join(err, recoveryErr)
				return
			}
			err = &RecoveryError{Path: path, Partial: pasting, Err: err}
		}
	}()
	stopped, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				_ = screen.PostEvent(tcell.NewEventInterrupt(ctx.Err()))
				select {
				case <-stopped:
					return
				case <-ticker.C:
				}
			}
		case <-stopped:
		}
	}()
	defer func() { close(stopped); <-joined }()
	e.setStatusMessage("HELP: Ctrl-S = save | Ctrl-Q / Ctrl-X = quit | Ctrl-F = find | Ctrl-C = cancel")
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !pasting {
			e.refreshScreen()
		}
		ev := screen.PollEvent()
		if ev == nil {
			return errors.New("editor terminal ended")
		}
		switch v := ev.(type) {
		case *rawPasteEvent:
			e.insertPaste(string(v.data))
			v.ack()
		case *inputFailureEvent:
			return v.err
		case *tcell.EventPaste:
			if v.Start() {
				if pasting {
					e.insertPaste(paste.String())
				}
				pasting = true
				paste.Reset()
			} else if pasting {
				e.insertPaste(paste.String())
				paste.Reset()
				pasting = false
			}
		case *tcell.EventResize:
			screen.Sync()
			e.resize()
		case *tcell.EventInterrupt:
			if err = ctx.Err(); err != nil {
				return err
			}
		case *tcell.EventKey:
			if pasting {
				paste.WriteString(pasteKey(v))
				continue
			}
			if !e.processKey(v) {
				if e.exitErr != nil {
					return e.exitErr
				}
				if e.dirty && e.saveErr != nil {
					return e.saveErr
				}
				return nil
			}
		}
	}
}

// tcell can fail before its shutdown channel exists. Attempt cleanup without
// turning an initialization error into an unhandled cleanup panic.
func finalizeScreen(screen tcell.Screen) (err error) {
	defer func() {
		if fault := recover(); fault != nil {
			err = fmt.Errorf("editor terminal cleanup failed: %v", fault)
		}
	}()
	screen.Fini()
	if final, ok := screen.(interface{ finalizationError() error }); ok {
		return final.finalizationError()
	}
	return nil
}

// ── Colour scheme ─────────────────────────────────────────────────────────────

func hlToStyle(hl hlType) tcell.Style {
	switch hl {
	case hlComment, hlMLComment:
		return tcell.StyleDefault.Foreground(tcell.ColorTeal)
	case hlKeyword1:
		return tcell.StyleDefault.Foreground(tcell.ColorYellow)
	case hlKeyword2:
		return tcell.StyleDefault.Foreground(tcell.ColorGreen)
	case hlString:
		return tcell.StyleDefault.Foreground(tcell.ColorPurple)
	case hlNumber:
		return tcell.StyleDefault.Foreground(tcell.ColorRed)
	case hlMatch:
		return tcell.StyleDefault.Foreground(tcell.NewRGBColor(100, 149, 237)) // cornflower blue
	case hlNonprint:
		return tcell.StyleDefault.Reverse(true)
	default:
		return tcell.StyleDefault
	}
}

// ── Syntax highlighting ───────────────────────────────────────────────────────

func isSeparator(c byte) bool {
	return c == 0 || c == ' ' || c == '\t' ||
		strings.ContainsRune(",.()+-/*=~%[];", rune(c))
}

func (s *editorSyntax) prepareKeywords() {
	if len(s.keywordInfo) == len(s.keywords) {
		return
	}

	s.keywordInfo = make([]syntaxKeyword, 0, len(s.keywords))
	for _, kw := range s.keywords {
		info := syntaxKeyword{word: kw, length: len(kw), hl: hlKeyword1}
		if info.length > 0 && kw[info.length-1] == '|' {
			info.word = kw[:info.length-1]
			info.length--
			info.hl = hlKeyword2
		}
		s.keywordInfo = append(s.keywordInfo, info)
	}
}

func asciiEqualFoldBytesString(buf []byte, s string) bool {
	if len(buf) != len(s) {
		return false
	}

	for i := range len(s) {
		b := buf[i]
		c := s[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if b != c {
			return false
		}
	}

	return true
}

func matchKeyword(render []byte, at int, keywords []syntaxKeyword) (hlType, int, bool) {
	for _, kw := range keywords {
		if at+kw.length > len(render) {
			continue
		}
		if !asciiEqualFoldBytesString(render[at:at+kw.length], kw.word) {
			continue
		}
		if at+kw.length < len(render) && !isSeparator(render[at+kw.length]) {
			continue
		}
		return kw.hl, kw.length, true
	}

	return hlNormal, 0, false
}

func (e *editor) selectSyntaxHighlight() {
	if e.filename == "" {
		return
	}
	for i := range hlDB {
		s := &hlDB[i]
		s.prepareKeywords()
		for _, pat := range s.fileMatch {
			if pat[0] == '.' {
				if strings.HasSuffix(e.filename, pat) {
					e.syntax = s
					return
				}
			} else {
				if strings.Contains(e.filename, pat) {
					e.syntax = s
					return
				}
			}
		}
	}
}

func (row *erow) hasOpenComment() bool {
	n := len(row.render)
	if n == 0 || len(row.hl) == 0 {
		return false
	}
	if row.hl[n-1] != hlMLComment {
		return false
	}
	// If the last two chars are "*/" the comment is closed.
	if n >= 2 && row.render[n-2] == '*' && row.render[n-1] == '/' {
		return false
	}
	return true
}

func (e *editor) updateSyntax(row *erow) {
	row.hl = make([]hlType, len(row.render))
	defer func() {
		for _, span := range row.nonprint {
			for i := span[0]; i < span[1]; i++ {
				row.hl[i] = hlNonprint
			}
		}
	}()

	if e.syntax == nil {
		return
	}

	scs := e.syntax.singleLineComment
	mcs := e.syntax.mlCommentStart
	mce := e.syntax.mlCommentEnd
	keywords := e.syntax.keywordInfo

	inString := byte(0)
	inComment := row.idx > 0 && e.rows[row.idx-1].hasOpenComment()
	prevSep := true
	r := row.render

	for i := 0; i < len(r); {
		c := r[i]

		// Single-line comment
		if prevSep && !inComment && inString == 0 && len(scs) >= 2 &&
			i+1 < len(r) && r[i] == scs[0] && r[i+1] == scs[1] {
			for j := i; j < len(r); j++ {
				row.hl[j] = hlComment
			}
			break
		}

		// Multi-line comment body
		if inString == 0 && inComment {
			row.hl[i] = hlMLComment
			if len(mce) >= 2 && i+1 < len(r) && r[i] == mce[0] && r[i+1] == mce[1] {
				row.hl[i+1] = hlMLComment
				i += 2
				inComment = false
				prevSep = true
				continue
			}
			prevSep = false
			i++
			continue
		}

		// Multi-line comment start
		if inString == 0 && len(mcs) >= 2 && i+1 < len(r) &&
			r[i] == mcs[0] && r[i+1] == mcs[1] {
			row.hl[i] = hlMLComment
			row.hl[i+1] = hlMLComment
			i += 2
			inComment = true
			prevSep = false
			continue
		}

		// String content
		if inString != 0 {
			row.hl[i] = hlString
			if c == '\\' && i+1 < len(r) {
				row.hl[i+1] = hlString
				i += 2
				prevSep = false
				continue
			}
			if c == inString {
				inString = 0
			}
			i++
			continue
		}

		// String start
		if e.syntax.flags&flagHighlightStrings != 0 && (c == '"' || c == '\'') {
			inString = c
			row.hl[i] = hlString
			i++
			prevSep = false
			continue
		}

		// Non-printable
		if c < 32 || c == 127 {
			row.hl[i] = hlNonprint
			i++
			prevSep = false
			continue
		}

		// Numbers
		if e.syntax.flags&flagHighlightNumbers != 0 {
			if (c >= '0' && c <= '9' && (prevSep || (i > 0 && row.hl[i-1] == hlNumber))) ||
				(c == '.' && i > 0 && row.hl[i-1] == hlNumber) {
				row.hl[i] = hlNumber
				i++
				prevSep = false
				continue
			}
		}

		// Keywords
		if prevSep {
			if ht, klen, matched := matchKeyword(r, i, keywords); matched {
				for j := range klen {
					row.hl[i+j] = ht
				}
				i += klen
				prevSep = false
				continue
			}
		}

		prevSep = isSeparator(c)
		i++
	}

	// Propagate open-comment state change to the next row.
	newOC := row.hasOpenComment()
	if row.hlOC != newOC && row.idx+1 < len(e.rows) {
		e.updateSyntax(e.rows[row.idx+1])
	}
	row.hlOC = newOC
}

// ── Row operations ────────────────────────────────────────────────────────────

func (e *editor) updateRow(row *erow) {
	var out strings.Builder
	row.nonprint = nil
	col := 0
	g := uniseg.NewGraphemes(string(row.chars))
	for g.Next() {
		if g.Str() == "\t" {
			n := 8 - col%8
			out.WriteString(strings.Repeat(" ", n))
			col += n
		} else {
			visible := displayCluster(g.Str())
			start := out.Len()
			out.WriteString(visible)
			if visible != g.Str() {
				row.nonprint = append(row.nonprint, [2]int{start, out.Len()})
			}
			col += uniseg.StringWidth(visible)
		}
	}
	row.render = []byte(out.String())
	e.updateSyntax(row)
}

func (e *editor) insertRow(at int, chars []byte) {
	if at > len(e.rows) {
		return
	}
	row := &erow{idx: at, chars: append([]byte(nil), chars...)}
	e.rows = append(e.rows, nil)
	copy(e.rows[at+1:], e.rows[at:])
	e.rows[at] = row
	for i := at + 1; i < len(e.rows); i++ {
		e.rows[i].idx++
	}
	e.updateRow(row)
	e.dirty = true
}

func (e *editor) delRow(at int) {
	if at >= len(e.rows) {
		return
	}
	e.rows = append(e.rows[:at], e.rows[at+1:]...)
	for i := at; i < len(e.rows); i++ {
		e.rows[i].idx--
	}
	e.dirty = true
}

func (e *editor) rowInsertChar(row *erow, at int, c byte) {
	if at > len(row.chars) {
		pad := at - len(row.chars)
		padBytes := make([]byte, pad)
		for i := range padBytes {
			padBytes[i] = ' '
		}
		row.chars = append(row.chars, padBytes...)
	}
	row.chars = append(row.chars, 0)
	copy(row.chars[at+1:], row.chars[at:])
	row.chars[at] = c
	e.updateRow(row)
	e.dirty = true
}

func (e *editor) rowAppendString(row *erow, s []byte) {
	row.chars = append(row.chars, s...)
	e.updateRow(row)
	e.dirty = true
}

func (e *editor) rowDelChar(row *erow, at int) {
	if at >= len(row.chars) {
		return
	}
	row.chars = append(row.chars[:at], row.chars[at+1:]...)
	e.updateRow(row)
	e.dirty = true
}

func (e *editor) rowsToString() string {
	var out strings.Builder
	for i, row := range e.rows {
		out.Write(row.chars)
		if i < len(e.rows)-1 || !e.omitFinalNewline {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// ── Editor-level insert / delete ─────────────────────────────────────────────

func (e *editor) insertChar(c byte) { e.insertText(string([]byte{c})) }
func (e *editor) insertText(text string) {
	row, col := e.position()
	if row == len(e.rows) {
		e.omitFinalNewline = true
	}
	for len(e.rows) <= row {
		e.insertRow(len(e.rows), nil)
	}
	r := e.rows[row]
	col = min(col, len(r.chars))
	r.chars = append(r.chars[:col], append([]byte(text), r.chars[col:]...)...)
	e.updateRow(r)
	e.dirty = true
	e.setPosition(row, boundaryAfter(string(r.chars), col+len(text)))
}

func (e *editor) insertNewline() {
	row, col := e.position()
	if row >= len(e.rows) {
		e.insertRow(len(e.rows), nil)
		e.omitFinalNewline = false
	} else {
		r := e.rows[row]
		col = min(col, len(r.chars))
		if col == 0 {
			e.insertRow(row, nil)
		} else {
			e.insertRow(row+1, r.chars[col:])
			r = e.rows[row]
			r.chars = r.chars[:col]
			e.updateRow(r)
		}
	}
	e.dirty = true
	e.colOff = 0
	e.setPosition(row+1, 0)
}

func (e *editor) delChar() {
	row, col := e.position()
	if row >= len(e.rows) {
		if row == len(e.rows) && len(e.rows) > 0 && !e.omitFinalNewline {
			e.omitFinalNewline = true
			e.dirty = true
			e.setPosition(row-1, len(e.rows[row-1].chars))
		}
		return
	}
	if row == 0 && col == 0 {
		return
	}
	if col == 0 {
		col = len(e.rows[row-1].chars)
		e.rowAppendString(e.rows[row-1], e.rows[row].chars)
		e.delRow(row)
		row--
	} else {
		r := e.rows[row]
		start := previousBoundary(string(r.chars), col)
		r.chars = append(r.chars[:start], r.chars[col:]...)
		col = start
		e.updateRow(r)
	}
	e.dirty = true
	e.setPosition(row, col)
}

// ── File I/O ──────────────────────────────────────────────────────────────────

func (e *editor) openFile(filename string) error {
	e.filename = filename
	e.rows = nil
	e.dirty = false
	e.omitFinalNewline = true

	f, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // new file
		}
		return err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			e.omitFinalNewline = line[len(line)-1] != '\n'
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			e.insertRow(len(e.rows), line)
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		return err
	}
	e.dirty = false
	return nil
}

func (e *editor) saveFile() {
	bytesWritten, err := e.writeRowsToFile()
	e.saveErr = err
	if err != nil {
		e.setStatusMessage("Can't save! I/O error: %v", err)
		return
	}
	e.dirty = false
	e.setStatusMessage("%d bytes written on disk", bytesWritten)
}

func (e *editor) writeRowsToFile() (n int, err error) {
	f, err := os.CreateTemp(filepath.Dir(e.filename), ".kilo-save-*.tmp")
	if err != nil {
		return 0, err
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	w := bufio.NewWriter(f)
	for i, row := range e.rows {
		written, writeErr := w.Write(row.chars)
		n += written
		if writeErr != nil {
			return n, writeErr
		}
		if i < len(e.rows)-1 || !e.omitFinalNewline {
			if err = w.WriteByte('\n'); err != nil {
				return n, err
			}
			n++
		}
	}
	if err = w.Flush(); err != nil {
		return n, err
	}
	if err = f.Sync(); err != nil {
		return n, err
	}
	if err = f.Close(); err != nil {
		return n, err
	}
	if err = os.Rename(temp, e.filename); err != nil {
		return n, err
	}
	return n, nil
}

// setStatusMessage sets the status message to be displayed in the message bar.
func (e *editor) setStatusMessage(format string, args ...any) {
	e.statusMsg = fmt.Sprintf(format, args...)
	e.statusMsgTime = time.Now()
}

// ── Rendering ─────────────────────────────────────────────────────────────────

func (e *editor) refreshScreen() {
	width, height := e.screen.Size()
	e.screenCols = width
	e.screenRows = max(height-2, 1) // two rows reserved for status + message bars
	row, col := e.position()
	e.setPosition(row, col)

	e.screen.Clear()

	// Content rows
	for y := range e.screenRows {
		filerow := e.rowOff + y

		if filerow >= len(e.rows) {
			if len(e.rows) == 0 && y == e.screenRows/3 {
				// Welcome message when no file content.
				welcome := fmt.Sprintf("Kilo editor -- version %s", kiloVersion)
				if len(welcome) > e.screenCols {
					welcome = welcome[:e.screenCols]
				}
				padding := (e.screenCols - len(welcome)) / 2
				x := 0
				if padding > 0 {
					e.screen.SetContent(x, y, '~', nil, tcell.StyleDefault)
					x++
					padding--
				}
				for ; padding > 0; padding-- {
					e.screen.SetContent(x, y, ' ', nil, tcell.StyleDefault)
					x++
				}
				for _, ch := range welcome {
					if x >= e.screenCols {
						break
					}
					e.screen.SetContent(x, y, ch, nil, tcell.StyleDefault)
					x++
				}
			} else {
				e.screen.SetContent(0, y, '~', nil, tcell.StyleDefault)
			}
			continue
		}

		e.drawRow(e.rows[filerow], y)

	}

	// Status bar (reverse video)
	statusY := e.screenRows
	statusStyle := tcell.StyleDefault.Reverse(true)
	left := fmt.Sprintf("%.20s - %d lines", e.filename, len(e.rows))
	if e.dirty {
		left += " (modified)"
	}
	right := fmt.Sprintf("%d/%d", e.rowOff+e.cy+1, len(e.rows))

	x := 0
	for _, ch := range left {
		if x >= e.screenCols {
			break
		}
		e.screen.SetContent(x, statusY, ch, nil, statusStyle)
		x++
	}
	// Pad middle
	for x < e.screenCols-len(right) {
		e.screen.SetContent(x, statusY, ' ', nil, statusStyle)
		x++
	}
	// Right-aligned line number
	for _, ch := range right {
		if x >= e.screenCols {
			break
		}
		e.screen.SetContent(x, statusY, ch, nil, statusStyle)
		x++
	}
	// Fill remainder
	for ; x < e.screenCols; x++ {
		e.screen.SetContent(x, statusY, ' ', nil, statusStyle)
	}

	// Message bar
	msgY := e.screenRows + 1
	if e.statusMsg != "" && time.Since(e.statusMsgTime) < 5*time.Second {
		x = 0
		for _, ch := range e.statusMsg {
			if x >= e.screenCols {
				break
			}
			e.screen.SetContent(x, msgY, ch, nil, tcell.StyleDefault)
			x++
		}
	}

	// Logical cursor and horizontal scroll are byte boundaries; projection is cells.
	row, col = e.position()
	cursorX := 0
	if row < len(e.rows) {
		r := string(e.rows[row].chars)
		cursorX = cellWidth(r[:min(col, len(r))]) - e.cellOff
	}
	e.screen.ShowCursor(max(cursorX, 0), e.cy)

	e.screen.Show()
}

// ── Cursor movement ───────────────────────────────────────────────────────────

func (e *editor) moveCursor(key tcell.Key) {
	row, col := e.position()
	switch key {
	case tcell.KeyLeft:
		if col > 0 && row < len(e.rows) {
			col = previousBoundary(string(e.rows[row].chars), col)
		} else if row > 0 {
			row--
			col = len(e.rows[row].chars)
		}
	case tcell.KeyRight:
		if row < len(e.rows) {
			if col < len(e.rows[row].chars) {
				col = nextBoundary(string(e.rows[row].chars), col)
			} else if row < e.lastRow() {
				row++
				col = 0
				e.colOff = 0
			}
		}
	case tcell.KeyUp:
		row = max(row-1, 0)
	case tcell.KeyDown:
		row = min(row+1, e.lastRow())
	}
	e.setPosition(row, col)
}

// ── Find / search ─────────────────────────────────────────────────────────────

func (e *editor) find() {
	e.finding = true
	defer func() {
		if e.exitErr == nil {
			e.finding = false
		}
	}()
	savedCX, savedCY := e.cx, e.cy
	savedColOff, savedRowOff := e.colOff, e.rowOff

	query := ""
	lastMatch := -1
	findNext := 0

	type savedHLState struct {
		line int
		hl   []hlType
	}
	var saved *savedHLState
	var pasting bool
	var paste strings.Builder

	restoreHL := func() {
		if saved == nil {
			return
		}
		copy(e.rows[saved.line].hl, saved.hl)
		saved = nil
	}

	for {
		if e.ctx != nil && e.ctx.Err() != nil {
			e.exitErr = e.ctx.Err()
			restoreHL()
			return
		}
		e.setStatusMessage("Search: %s (Use ESC/Arrows/Enter)", query)
		if !pasting {
			e.refreshScreen()
		}

		ev := e.screen.PollEvent()
		if ev == nil {
			e.exitErr = errors.New("editor terminal ended during find")
			restoreHL()
			return
		}
		switch ev := ev.(type) {
		case *rawPasteEvent:
			query += strings.NewReplacer("\n", " ", "\t", " ").Replace(terminalText(normalizePaste(string(ev.data))))
			ev.ack()
			lastMatch = -1
		case *inputFailureEvent:
			e.exitErr = ev.err
			restoreHL()
			return
		case *tcell.EventPaste:
			if ev.Start() {
				pasting = true
				paste.Reset()
			} else if pasting {
				query += strings.NewReplacer("\n", " ", "\t", " ").Replace(terminalText(normalizePaste(paste.String())))
				pasting = false
				lastMatch = -1
			}
		case *tcell.EventInterrupt:
			if e.ctx != nil && e.ctx.Err() != nil {
				e.exitErr = e.ctx.Err()
				restoreHL()
				return
			}
		case *tcell.EventResize:
			e.screen.Sync()
			e.resize()
			continue
		case *tcell.EventKey:
			if pasting {
				paste.WriteString(pasteKey(ev))
				continue
			}
			switch ev.Key() {
			case tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyDelete:
				if len(query) > 0 {
					query = query[:previousBoundary(query, len(query))]
					lastMatch = -1
				}
			case tcell.KeyCtrlC:
				e.exitErr = context.Canceled
				restoreHL()
				return
			case tcell.KeyEscape:
				e.cx, e.cy = savedCX, savedCY
				e.colOff, e.rowOff = savedColOff, savedRowOff
				restoreHL()
				e.setStatusMessage("")
				return
			case tcell.KeyEnter:
				restoreHL()
				e.setStatusMessage("")
				return
			case tcell.KeyRight, tcell.KeyDown:
				findNext = 1
			case tcell.KeyLeft, tcell.KeyUp:
				findNext = -1
			default:
				if ev.Rune() != 0 && !unicode.IsControl(ev.Rune()) && terminalText(string(ev.Rune())) == string(ev.Rune()) {
					query += string(ev.Rune())
					lastMatch = -1
				}
			}
		}

		if lastMatch == -1 {
			findNext = 1
		}
		if findNext == 0 || len(query) == 0 {
			continue
		}

		current := lastMatch
		matchLine := -1
		matchOffset := -1
		queryBytes := []byte(query)
		for i := 0; i < len(e.rows); i++ {
			current += findNext
			if current < 0 {
				current = len(e.rows) - 1
			} else if current >= len(e.rows) {
				current = 0
			}
			idx := bytes.Index(e.rows[current].render, queryBytes)
			if idx >= 0 {
				matchLine = current
				matchOffset = idx
				break
			}
		}
		findNext = 0
		restoreHL()

		if matchLine >= 0 {
			lastMatch = matchLine
			row := e.rows[matchLine]
			// Save and overwrite highlight for the matched span.
			hlCopy := make([]hlType, len(row.hl))
			copy(hlCopy, row.hl)
			saved = &savedHLState{line: matchLine, hl: hlCopy}
			end := min(matchOffset+len(query), len(row.hl))
			for j := matchOffset; j < end; j++ {
				row.hl[j] = hlMatch
			}
			// Match offsets are rendered bytes; map cells back to a source boundary.
			cells := uniseg.StringWidth(string(row.render[:matchOffset]))
			e.setPosition(matchLine, rawColumnAtCell(string(row.chars), cells))

		}
	}
}

// ── Key processing ────────────────────────────────────────────────────────────

// processKey handles one key event; returns false when the editor should exit.
func (e *editor) processKey(ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyEnter:
		e.insertNewline()

	case tcell.KeyCtrlQ, tcell.KeyCtrlX:
		if e.dirty && e.quitTimes > 0 {
			e.setStatusMessage(
				"WARNING!!! File has unsaved changes. Press Ctrl-Q %d more times to quit.",
				e.quitTimes)
			e.quitTimes--
			return true
		}
		return false

	case tcell.KeyCtrlS:
		e.saveFile()

	case tcell.KeyCtrlF:
		e.find()
		if e.exitErr != nil {
			return false
		}

	case tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyDelete, tcell.KeyCtrlH:
		e.delChar()

	case tcell.KeyPgUp:
		if e.cy != 0 {
			e.cy = 0
		}
		for i := 0; i < e.screenRows; i++ {
			e.moveCursor(tcell.KeyUp)
		}

	case tcell.KeyPgDn:
		if e.cy != e.screenRows-1 {
			e.cy = e.screenRows - 1
		}
		for i := 0; i < e.screenRows; i++ {
			e.moveCursor(tcell.KeyDown)
		}

	case tcell.KeyUp, tcell.KeyDown, tcell.KeyLeft, tcell.KeyRight:
		e.moveCursor(ev.Key())

	case tcell.KeyHome:
		e.colOff = 0
		e.setPosition(e.rowOff+e.cy, 0)
	case tcell.KeyEnd:
		row, _ := e.position()
		if row < len(e.rows) {
			e.setPosition(row, len(e.rows[row].chars))
		}

	case tcell.KeyEscape, tcell.KeyCtrlL:
		// nothing

	case tcell.KeyCtrlC:
		e.exitErr = context.Canceled
		return false
	case tcell.KeyTab:
		e.insertText("\t")

	default:
		if r := ev.Rune(); r != 0 && (r == '\t' || !unicode.IsControl(r) && terminalText(string(r)) == string(r)) {
			e.insertText(string(r))
		}

	}

	e.quitTimes = kiloQuitTimes
	return true
}
