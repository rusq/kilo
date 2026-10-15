package kilo

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

func previousBoundary(s string, at int) int {
	previous := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		_, end := g.Positions()
		if end >= at {
			return previous
		}
		previous = end
	}
	return previous
}
func nextBoundary(s string, at int) int {
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		_, end := g.Positions()
		if end > at {
			return end
		}
	}
	return len(s)
}
func boundaryBefore(s string, at int) int {
	if at >= len(s) {
		return len(s)
	}
	if at <= 0 {
		return 0
	}
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		start, end := g.Positions()
		if end == at {
			return at
		}
		if end > at {
			return start
		}
	}
	return len(s)
}
func boundaryAfter(s string, at int) int {
	if at <= 0 {
		return 0
	}
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		_, end := g.Positions()
		if end >= at {
			return end
		}
	}
	return len(s)
}
func cellWidth(s string) int {
	cells := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		if g.Str() == "\t" {
			cells += 8 - cells%8
		} else {
			cells += uniseg.StringWidth(displayCluster(g.Str()))
		}
	}
	return cells
}
func rawColumnAtCell(s string, want int) int {
	cells := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		start, end := g.Positions()
		w := uniseg.StringWidth(displayCluster(g.Str()))
		if g.Str() == "\t" {
			w = 8 - cells%8
		}
		if cells+w > want {
			return start
		}
		cells += w
		if cells == want {
			return end
		}
	}
	return len(s)
}
func (e *editor) lastRow() int {
	if e.omitFinalNewline {
		return max(len(e.rows)-1, 0)
	}
	return len(e.rows)
}
func (e *editor) position() (int, int) {
	row := max(e.rowOff+e.cy, 0)
	col := max(e.colOff+e.cx, 0)
	if row < len(e.rows) {
		s := string(e.rows[row].chars)
		col = boundaryBefore(s, col)
		e.cellOff = cellWidth(s[:boundaryBefore(s, min(e.colOff, len(s)))])
	} else {
		col = 0
	}
	return row, col
}
func (e *editor) setPosition(row, col int) {
	row = min(max(row, 0), e.lastRow())
	col = max(col, 0)
	if row < len(e.rows) {
		s := string(e.rows[row].chars)
		col = boundaryBefore(s, col)
		total := cellWidth(s[:col])
		e.colOff = rawColumnAtCell(s, min(e.cellOff, total))
		e.colOff = min(e.colOff, col)
		limit := max(e.screenCols-1, 0)
		left := cellWidth(s[:e.colOff])
		origin := e.colOff
		g := uniseg.NewGraphemes(s[origin:col])
		for total-left > limit && g.Next() {
			w := uniseg.StringWidth(displayCluster(g.Str()))
			if g.Str() == "\t" {
				w = 8 - left%8
			}
			left += w
			_, end := g.Positions()
			e.colOff = origin + end
		}
		e.cellOff = left

	} else {
		col = 0
		e.colOff = 0
		e.cellOff = 0
	}
	if row < e.rowOff {
		e.rowOff = row
	}
	if row >= e.rowOff+max(e.screenRows, 1) {
		e.rowOff = row - max(e.screenRows, 1) + 1
	}
	e.cy = row - e.rowOff
	e.cx = col - e.colOff
}
func (e *editor) resize() {
	row, col := e.position()
	w, h := e.screen.Size()
	e.screenCols = max(w, 1)
	e.screenRows = max(h-2, 1)
	e.setPosition(row, col)
}
func (e *editor) drawRow(row *erow, y int) {
	left := e.cellOff
	cells := 0
	g := uniseg.NewGraphemes(string(row.render))
	for g.Next() {
		start, _ := g.Positions()
		cluster := g.Str()
		w := max(g.Width(), 1)
		if cells+w <= left {
			cells += w
			continue
		}
		x := cells - left
		if x < 0 {
			cells += w
			continue
		}
		if x+w > e.screenCols {
			break
		}
		hl := hlNormal
		if start < len(row.hl) {
			hl = row.hl[start]
		}
		if g.Width() == 0 {
			cluster = " " + cluster
			w = 1
		}
		e.screen.Put(x, y, cluster, hlToStyle(hl))
		cells += w
	}
}

func displayCluster(s string) string {
	s = terminalText(s)
	if uniseg.StringWidth(s) > 0 {
		return s
	}
	r, _ := utf8.DecodeRuneInString(s)
	if unicode.IsMark(r) {
		return "◌" + s
	}
	return fmt.Sprintf("\\u%04x", r)
}

func (e *editor) insertPaste(text string) {
	text = normalizePaste(text)
	if text == "" {
		return
	}
	parts := strings.Split(text, "\n")
	if len(parts) == 1 {
		e.insertText(text)
		return
	}
	row, col := e.position()
	if row == len(e.rows) {
		e.omitFinalNewline = true
	}
	for len(e.rows) <= row {
		e.insertRow(len(e.rows), nil)
	}
	old := e.rows[row]
	prefix := append([]byte{}, old.chars[:col]...)
	suffix := append([]byte{}, old.chars[col:]...)
	inserted := make([]*erow, len(parts))
	for i, part := range parts {
		inserted[i] = &erow{chars: []byte(part)}
	}
	inserted[0].chars = append(prefix, inserted[0].chars...)
	last := len(inserted) - 1
	caret := len(inserted[last].chars)
	inserted[last].chars = append(inserted[last].chars, suffix...)
	rows := make([]*erow, 0, len(e.rows)+len(parts)-1)
	rows = append(rows, e.rows[:row]...)
	rows = append(rows, inserted...)
	rows = append(rows, e.rows[row+1:]...)
	e.rows = rows
	for i, r := range e.rows {
		r.idx = i
	}
	for _, r := range inserted {
		e.updateRow(r)
	}
	e.dirty = true
	e.setPosition(row+last, boundaryAfter(string(inserted[last].chars), caret))
}

func normalizePaste(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

func pasteKey(key *tcell.EventKey) string {
	switch {
	case key.Key() == tcell.KeyRune:
		return string(key.Rune())
	case key.Key() == tcell.KeyEnter:
		return "\r"
	case key.Key() >= 0 && key.Key() < 32:
		return string(rune(key.Key()))
	case key.Key() >= tcell.KeyCtrlSpace && key.Key() <= tcell.KeyCtrlUnderscore:
		return string(rune(key.Key() - tcell.KeyCtrlSpace))
	default:
		return ""
	}
}
