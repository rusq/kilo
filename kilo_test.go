package kilo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestSyntaxHighlighting(t *testing.T) {
	t.Run("SelectSyntaxHighlight", func(t *testing.T) {
		tests := []struct {
			name                    string
			filename                string
			wantNil                 bool
			wantSingleLineComment   string
			wantFlags               int
			wantMultiLineCommentEnd string
		}{
			{
				name:                    "sql extension",
				filename:                "query.sql",
				wantSingleLineComment:   "--",
				wantFlags:               flagHighlightStrings | flagHighlightNumbers,
				wantMultiLineCommentEnd: "*/",
			},
			{
				name:                    "sqlx extension",
				filename:                "query.sqlx",
				wantSingleLineComment:   "--",
				wantFlags:               flagHighlightStrings | flagHighlightNumbers,
				wantMultiLineCommentEnd: "*/",
			},
			{
				name:                    "c extension",
				filename:                "main.c",
				wantSingleLineComment:   "//",
				wantFlags:               flagHighlightStrings | flagHighlightNumbers,
				wantMultiLineCommentEnd: "*/",
			},
			{
				name:     "unknown extension",
				filename: "notes.txt",
				wantNil:  true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, tt.filename, nil)
				e.selectSyntaxHighlight()

				if tt.wantNil {
					if e.syntax != nil {
						t.Fatalf("syntax = %#v, want nil", e.syntax)
					}
					return
				}

				if e.syntax == nil {
					t.Fatal("syntax = nil, want non-nil")
				}
				if e.syntax.singleLineComment != tt.wantSingleLineComment {
					t.Fatalf("singleLineComment = %q, want %q", e.syntax.singleLineComment, tt.wantSingleLineComment)
				}
				if e.syntax.flags != tt.wantFlags {
					t.Fatalf("flags = %d, want %d", e.syntax.flags, tt.wantFlags)
				}
				if e.syntax.mlCommentEnd != tt.wantMultiLineCommentEnd {
					t.Fatalf("mlCommentEnd = %q, want %q", e.syntax.mlCommentEnd, tt.wantMultiLineCommentEnd)
				}
			})
		}
	})

	t.Run("Keywords", func(t *testing.T) {
		type keywordSpan struct {
			start int
			end   int
			want  hlType
		}

		tests := []struct {
			name             string
			syntax           *editorSyntax
			line             string
			wantKeywordSpans []keywordSpan
			wantNormalSpans  [][2]int
		}{
			{
				name:             "sql keyword lowercase",
				syntax:           &hlDB[1],
				line:             "select from",
				wantKeywordSpans: []keywordSpan{{start: 0, end: 6, want: hlKeyword1}, {start: 7, end: 11, want: hlKeyword1}},
				wantNormalSpans:  [][2]int{{6, 7}},
			},
			{
				name:             "sql keyword uppercase",
				syntax:           &hlDB[1],
				line:             "SELECT FROM",
				wantKeywordSpans: []keywordSpan{{start: 0, end: 6, want: hlKeyword1}, {start: 7, end: 11, want: hlKeyword1}},
				wantNormalSpans:  [][2]int{{6, 7}},
			},
			{
				name:             "sql keyword mixed case",
				syntax:           &hlDB[1],
				line:             "SeLeCt FrOm",
				wantKeywordSpans: []keywordSpan{{start: 0, end: 6, want: hlKeyword1}, {start: 7, end: 11, want: hlKeyword1}},
				wantNormalSpans:  [][2]int{{6, 7}},
			},
			{
				name:             "boundary positive",
				syntax:           &hlDB[1],
				line:             "select,",
				wantKeywordSpans: []keywordSpan{{start: 0, end: 6, want: hlKeyword1}},
				wantNormalSpans:  [][2]int{{6, 7}},
			},
			{
				name:            "boundary negative suffix",
				syntax:          &hlDB[1],
				line:            "selected",
				wantNormalSpans: [][2]int{{0, 8}},
			},
			{
				name:            "boundary negative prefix",
				syntax:          &hlDB[1],
				line:            "xselect",
				wantNormalSpans: [][2]int{{0, 7}},
			},
			{
				name:             "c keyword2 type",
				syntax:           &hlDB[0],
				line:             "int value",
				wantKeywordSpans: []keywordSpan{{start: 0, end: 3, want: hlKeyword2}},
				wantNormalSpans:  [][2]int{{3, 9}},
			},
			{
				name:             "c corrected decltype keyword",
				syntax:           &hlDB[0],
				line:             "decltype value",
				wantKeywordSpans: []keywordSpan{{start: 0, end: 8, want: hlKeyword1}},
				wantNormalSpans:  [][2]int{{8, 14}},
			},
			{
				name:             "sql corrected exists keyword",
				syntax:           &hlDB[1],
				line:             "exists value",
				wantKeywordSpans: []keywordSpan{{start: 0, end: 6, want: hlKeyword1}},
				wantNormalSpans:  [][2]int{{6, 12}},
			},
			{
				name:            "c misspelt deltype is not keyword",
				syntax:          &hlDB[0],
				line:            "deltype value",
				wantNormalSpans: [][2]int{{0, 13}},
			},
			{
				name:            "sql misspelt exist is not keyword",
				syntax:          &hlDB[1],
				line:            "exist value",
				wantNormalSpans: [][2]int{{0, 11}},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, "", tt.syntax)
				addRows(e, tt.line)

				row := e.rows[0]
				for _, span := range tt.wantKeywordSpans {
					assertAllHL(t, row, span.start, span.end, span.want)
				}
				for _, span := range tt.wantNormalSpans {
					assertAllHL(t, row, span[0], span[1], hlNormal)
				}
			})
		}
	})

	t.Run("StringsNumbersComments", func(t *testing.T) {
		tests := []struct {
			name   string
			syntax *editorSyntax
			line   []byte
			assert func(t *testing.T, row *erow)
		}{
			{
				name:   "sql string literal",
				syntax: &hlDB[1],
				line:   []byte("'abc'"),
				assert: func(t *testing.T, row *erow) {
					assertAllHL(t, row, 0, 5, hlString)
				},
			},
			{
				name:   "c escaped quote string",
				syntax: &hlDB[0],
				line:   []byte("\"a\\\"b\""),
				assert: func(t *testing.T, row *erow) {
					assertAllHL(t, row, 0, len(row.render), hlString)
				},
			},
			{
				name:   "integer literal",
				syntax: &hlDB[1],
				line:   []byte("select 123"),
				assert: func(t *testing.T, row *erow) {
					assertAllHL(t, row, 0, 6, hlKeyword1)
					assertAllHL(t, row, 6, 7, hlNormal)
					assertAllHL(t, row, 7, 10, hlNumber)
				},
			},
			{
				name:   "decimal literal",
				syntax: &hlDB[1],
				line:   []byte("3.14"),
				assert: func(t *testing.T, row *erow) {
					assertAllHL(t, row, 0, 4, hlNumber)
				},
			},
			{
				name:   "sql single line comment",
				syntax: &hlDB[1],
				line:   []byte("select -- hello"),
				assert: func(t *testing.T, row *erow) {
					assertAllHL(t, row, 0, 6, hlKeyword1)
					assertAllHL(t, row, 6, 7, hlNormal)
					assertAllHL(t, row, 7, len(row.render), hlComment)
				},
			},
			{
				name:   "control byte",
				syntax: &hlDB[1],
				line:   []byte{'a', 1, 'b'},
				assert: func(t *testing.T, row *erow) {
					if string(row.render) != `a\u0001b` {
						t.Fatalf("unsafe control projection: %q", row.render)
					}
					assertAllHL(t, row, 0, 1, hlNormal)
					assertAllHL(t, row, 1, 7, hlNonprint)
					assertAllHL(t, row, 7, 8, hlNormal)
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, "", tt.syntax)
				addRows(e, string(tt.line))
				tt.assert(t, e.rows[0])
			})
		}
	})

	t.Run("MultiLineComments", func(t *testing.T) {
		type rowCheck struct {
			row           int
			wantOpen      bool
			commentRanges [][2]int
			normalRanges  [][2]int
		}

		t.Run("StaticCases", func(t *testing.T) {
			tests := []struct {
				name   string
				lines  []string
				checks []rowCheck
			}{
				{
					name:  "open continue close",
					lines: []string{"/* one", "two", "three */"},
					checks: []rowCheck{
						{row: 0, wantOpen: true, commentRanges: [][2]int{{0, 6}}},
						{row: 1, wantOpen: true, commentRanges: [][2]int{{0, 3}}},
						{row: 2, wantOpen: false, commentRanges: [][2]int{{0, 8}}},
					},
				},
				{
					name:  "open and close same row",
					lines: []string{"/* x */ y"},
					checks: []rowCheck{
						{row: 0, wantOpen: false, commentRanges: [][2]int{{0, 7}}, normalRanges: [][2]int{{7, 9}}},
					},
				},
				{
					name:  "closing row followed by normal row",
					lines: []string{"/* open", "close */", "plain"},
					checks: []rowCheck{
						{row: 0, wantOpen: true, commentRanges: [][2]int{{0, 7}}},
						{row: 1, wantOpen: false, commentRanges: [][2]int{{0, 8}}},
						{row: 2, wantOpen: false, normalRanges: [][2]int{{0, 5}}},
					},
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					e := newTestEditor(0, 0, "", &hlDB[0], tt.lines...)

					for _, check := range tt.checks {
						row := e.rows[check.row]
						if row.hlOC != check.wantOpen {
							t.Fatalf("row %d hlOC = %v, want %v", check.row, row.hlOC, check.wantOpen)
						}
						for _, span := range check.commentRanges {
							assertAllHL(t, row, span[0], span[1], hlMLComment)
						}
						for _, span := range check.normalRanges {
							assertAllHL(t, row, span[0], span[1], hlNormal)
						}
					}
				})
			}
		})

		t.Run("Propagation", func(t *testing.T) {
			tests := []struct {
				name            string
				initial         []string
				updatedFirstRow string
				checks          []rowCheck
			}{
				{
					name:            "open comment state propagates to following rows",
					initial:         []string{"/* open", "still", "plain"},
					updatedFirstRow: "/* open */",
					checks: []rowCheck{
						{row: 0, wantOpen: false, commentRanges: [][2]int{{0, 10}}},
						{row: 1, wantOpen: false, normalRanges: [][2]int{{0, 5}}},
						{row: 2, wantOpen: false, normalRanges: [][2]int{{0, 5}}},
					},
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					e := newTestEditor(0, 0, "", &hlDB[0], tt.initial...)
					e.rows[0].chars = []byte(tt.updatedFirstRow)
					e.updateRow(e.rows[0])

					for _, check := range tt.checks {
						row := e.rows[check.row]
						if row.hlOC != check.wantOpen {
							t.Fatalf("row %d hlOC = %v, want %v", check.row, row.hlOC, check.wantOpen)
						}
						for _, span := range check.commentRanges {
							assertAllHL(t, row, span[0], span[1], hlMLComment)
						}
						for _, span := range check.normalRanges {
							assertAllHL(t, row, span[0], span[1], hlNormal)
						}
					}
				})
			}
		})
	})

	t.Run("Separators", func(t *testing.T) {
		tests := []struct {
			name string
			b    byte
			want bool
		}{
			{name: "space", b: ' ', want: true},
			{name: "tab", b: '\t', want: true},
			{name: "comma", b: ',', want: true},
			{name: "semicolon", b: ';', want: true},
			{name: "open paren", b: '(', want: true},
			{name: "close paren", b: ')', want: true},
			{name: "letter lower", b: 'a', want: false},
			{name: "letter upper", b: 'Z', want: false},
			{name: "digit", b: '0', want: false},
			{name: "underscore", b: '_', want: false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := isSeparator(tt.b); got != tt.want {
					t.Fatalf("isSeparator(%q) = %v, want %v", tt.b, got, tt.want)
				}
			})
		}
	})
}

func TestSyntaxHelpers(t *testing.T) {
	t.Run("PrepareKeywords", func(t *testing.T) {
		syntax := editorSyntax{keywords: []string{"select", "int|"}}

		syntax.prepareKeywords()

		if len(syntax.keywordInfo) != 2 {
			t.Fatalf("len(keywordInfo) = %d, want 2", len(syntax.keywordInfo))
		}
		if syntax.keywordInfo[0] != (syntaxKeyword{word: "select", length: 6, hl: hlKeyword1}) {
			t.Fatalf("keywordInfo[0] = %#v", syntax.keywordInfo[0])
		}
		if syntax.keywordInfo[1] != (syntaxKeyword{word: "int", length: 3, hl: hlKeyword2}) {
			t.Fatalf("keywordInfo[1] = %#v", syntax.keywordInfo[1])
		}
	})

	t.Run("ASCIIEqualFoldBytesString", func(t *testing.T) {
		tests := []struct {
			name string
			buf  []byte
			s    string
			want bool
		}{
			{name: "same lowercase", buf: []byte("select"), s: "select", want: true},
			{name: "ascii case insensitive", buf: []byte("SeLeCt"), s: "select", want: true},
			{name: "different value", buf: []byte("select"), s: "insert", want: false},
			{name: "different length", buf: []byte("select"), s: "selects", want: false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := asciiEqualFoldBytesString(tt.buf, tt.s); got != tt.want {
					t.Fatalf("asciiEqualFoldBytesString(%q, %q) = %v, want %v", tt.buf, tt.s, got, tt.want)
				}
			})
		}
	})

	t.Run("MatchKeyword", func(t *testing.T) {
		keywords := []syntaxKeyword{{word: "select", length: 6, hl: hlKeyword1}, {word: "int", length: 3, hl: hlKeyword2}}

		tests := []struct {
			name     string
			render   []byte
			at       int
			wantHL   hlType
			wantLen  int
			wantOkay bool
		}{
			{name: "match primary keyword", render: []byte("SELECT value"), at: 0, wantHL: hlKeyword1, wantLen: 6, wantOkay: true},
			{name: "match secondary keyword", render: []byte("int value"), at: 0, wantHL: hlKeyword2, wantLen: 3, wantOkay: true},
			{name: "reject suffix continuation", render: []byte("selected"), at: 0, wantHL: hlNormal, wantLen: 0, wantOkay: false},
			{name: "reject non match", render: []byte("value"), at: 0, wantHL: hlNormal, wantLen: 0, wantOkay: false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				gotHL, gotLen, gotOkay := matchKeyword(tt.render, tt.at, keywords)
				if gotHL != tt.wantHL || gotLen != tt.wantLen || gotOkay != tt.wantOkay {
					t.Fatalf("matchKeyword(%q, %d) = (%v, %d, %v), want (%v, %d, %v)", tt.render, tt.at, gotHL, gotLen, gotOkay, tt.wantHL, tt.wantLen, tt.wantOkay)
				}
			})
		}
	})
}

func TestRowOperations(t *testing.T) {
	t.Run("UpdateRow", func(t *testing.T) {
		tests := []struct {
			name       string
			chars      string
			wantRender string
		}{
			{name: "no tabs", chars: "abc", wantRender: "abc"},
			{name: "tab at column zero", chars: "\t", wantRender: "        "},
			{name: "tab after one char", chars: "a\t", wantRender: "a       "},
			{name: "multiple tabs", chars: "a\tb\t", wantRender: "a       b       "},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, "", nil)
				row := &erow{chars: []byte(tt.chars)}
				e.updateRow(row)

				if got := string(row.render); got != tt.wantRender {
					t.Fatalf("render = %q, want %q", got, tt.wantRender)
				}
				if len(row.hl) != len(row.render) {
					t.Fatalf("len(hl) = %d, want %d", len(row.hl), len(row.render))
				}
			})
		}
	})

	t.Run("InsertRow", func(t *testing.T) {
		tests := []struct {
			name    string
			initial []string
			at      int
			insert  string
			want    []string
		}{
			{name: "insert at start", initial: []string{"b", "c"}, at: 0, insert: "a", want: []string{"a", "b", "c"}},
			{name: "insert in middle", initial: []string{"a", "c"}, at: 1, insert: "b", want: []string{"a", "b", "c"}},
			{name: "insert at end", initial: []string{"a", "b"}, at: 2, insert: "c", want: []string{"a", "b", "c"}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, "", nil, tt.initial...)
				e.dirty = false
				e.insertRow(tt.at, []byte(tt.insert))

				if len(e.rows) != len(tt.want) {
					t.Fatalf("row count = %d, want %d", len(e.rows), len(tt.want))
				}
				for i, want := range tt.want {
					if got := rowText(e, i); got != want {
						t.Fatalf("row %d = %q, want %q", i, got, want)
					}
					if e.rows[i].idx != i {
						t.Fatalf("row %d idx = %d, want %d", i, e.rows[i].idx, i)
					}
				}
				if !e.dirty {
					t.Fatal("dirty = false, want true")
				}
			})
		}
	})

	t.Run("DeleteRow", func(t *testing.T) {
		tests := []struct {
			name      string
			initial   []string
			at        int
			want      []string
			wantDirty bool
		}{
			{name: "delete first row", initial: []string{"a", "b", "c"}, at: 0, want: []string{"b", "c"}, wantDirty: true},
			{name: "delete middle row", initial: []string{"a", "b", "c"}, at: 1, want: []string{"a", "c"}, wantDirty: true},
			{name: "delete last row", initial: []string{"a", "b", "c"}, at: 2, want: []string{"a", "b"}, wantDirty: true},
			{name: "delete out of range", initial: []string{"a", "b"}, at: 5, want: []string{"a", "b"}, wantDirty: false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, "", nil, tt.initial...)
				e.dirty = false
				e.delRow(tt.at)

				if len(e.rows) != len(tt.want) {
					t.Fatalf("row count = %d, want %d", len(e.rows), len(tt.want))
				}
				for i, want := range tt.want {
					if got := rowText(e, i); got != want {
						t.Fatalf("row %d = %q, want %q", i, got, want)
					}
					if e.rows[i].idx != i {
						t.Fatalf("row %d idx = %d, want %d", i, e.rows[i].idx, i)
					}
				}
				if e.dirty != tt.wantDirty {
					t.Fatalf("dirty = %v, want %v", e.dirty, tt.wantDirty)
				}
			})
		}
	})

	t.Run("RowsToString", func(t *testing.T) {
		tests := []struct {
			name string
			rows []string
			want string
		}{
			{name: "empty buffer", want: ""},
			{name: "one row", rows: []string{"a"}, want: "a\n"},
			{name: "multiple rows", rows: []string{"a", "b", "c"}, want: "a\nb\nc\n"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, "", nil, tt.rows...)
				if got := e.rowsToString(); got != tt.want {
					t.Fatalf("rowsToString() = %q, want %q", got, tt.want)
				}
			})
		}
	})
}

func TestEditingOperations(t *testing.T) {
	t.Run("InsertNewline", func(t *testing.T) {
		tests := []struct {
			name       string
			initial    []string
			cx         int
			cy         int
			colOff     int
			rowOff     int
			wantRows   []string
			wantCX     int
			wantCY     int
			wantColOff int
			wantRowOff int
		}{
			{name: "split middle of row", initial: []string{"hello"}, cx: 2, cy: 0, wantRows: []string{"he", "llo"}, wantCX: 0, wantCY: 1},
			{name: "split at start of row", initial: []string{"hello"}, cx: 0, cy: 0, wantRows: []string{"", "hello"}, wantCX: 0, wantCY: 1},
			{name: "split at end of row", initial: []string{"hello"}, cx: 5, cy: 0, wantRows: []string{"hello", ""}, wantCX: 0, wantCY: 1},
			{name: "newline on empty buffer", cx: 0, cy: 0, wantRows: []string{""}, wantCX: 0, wantCY: 1},
			{name: "newline at eof row via cy", initial: []string{"one"}, cx: 0, cy: 1, wantRows: []string{"one", ""}, wantCX: 0, wantCY: 2},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, "", nil, tt.initial...)
				e.cx = tt.cx
				e.cy = tt.cy
				e.colOff = tt.colOff
				e.rowOff = tt.rowOff
				e.dirty = false

				e.insertNewline()

				if len(e.rows) != len(tt.wantRows) {
					t.Fatalf("row count = %d, want %d", len(e.rows), len(tt.wantRows))
				}
				for i, want := range tt.wantRows {
					if got := rowText(e, i); got != want {
						t.Fatalf("row %d = %q, want %q", i, got, want)
					}
				}
				if e.cx != tt.wantCX || e.cy != tt.wantCY || e.colOff != tt.wantColOff || e.rowOff != tt.wantRowOff {
					t.Fatalf("state = (cx=%d cy=%d colOff=%d rowOff=%d), want (%d %d %d %d)", e.cx, e.cy, e.colOff, e.rowOff, tt.wantCX, tt.wantCY, tt.wantColOff, tt.wantRowOff)
				}
				if !e.dirty {
					t.Fatal("dirty = false, want true")
				}
			})
		}
	})

	t.Run("DeleteChar", func(t *testing.T) {
		tests := []struct {
			name       string
			initial    []string
			cx         int
			cy         int
			colOff     int
			rowOff     int
			wantRows   []string
			wantCX     int
			wantCY     int
			wantColOff int
			wantRowOff int
			wantDirty  bool
		}{
			{name: "delete within a line", initial: []string{"hello"}, cx: 3, cy: 0, wantRows: []string{"helo"}, wantCX: 2, wantCY: 0, wantDirty: true},
			{name: "merge into previous line", initial: []string{"ab", "cd"}, cx: 0, cy: 1, wantRows: []string{"abcd"}, wantCX: 2, wantCY: 0, wantDirty: true},
			{name: "backspace at start of file is no op", initial: []string{"ab"}, cx: 0, cy: 0, wantRows: []string{"ab"}, wantCX: 0, wantCY: 0, wantDirty: false},
			{name: "delete with horizontal scroll", initial: []string{"abcdef"}, cx: 0, cy: 0, colOff: 2, wantRows: []string{"acdef"}, wantCX: 0, wantCY: 0, wantColOff: 1, wantDirty: true},
			{name: "delete final newline at eof", initial: []string{"ab"}, cx: 0, cy: 1, wantRows: []string{"ab"}, wantCX: 2, wantCY: 0, wantDirty: true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(0, 0, "", nil, tt.initial...)
				e.cx = tt.cx
				e.cy = tt.cy
				e.colOff = tt.colOff
				e.rowOff = tt.rowOff
				e.dirty = false

				e.delChar()

				if len(e.rows) != len(tt.wantRows) {
					t.Fatalf("row count = %d, want %d", len(e.rows), len(tt.wantRows))
				}
				for i, want := range tt.wantRows {
					if got := rowText(e, i); got != want {
						t.Fatalf("row %d = %q, want %q", i, got, want)
					}
				}
				if e.cx != tt.wantCX || e.cy != tt.wantCY || e.colOff != tt.wantColOff || e.rowOff != tt.wantRowOff {
					t.Fatalf("state = (cx=%d cy=%d colOff=%d rowOff=%d), want (%d %d %d %d)", e.cx, e.cy, e.colOff, e.rowOff, tt.wantCX, tt.wantCY, tt.wantColOff, tt.wantRowOff)
				}
				if e.dirty != tt.wantDirty {
					t.Fatalf("dirty = %v, want %v", e.dirty, tt.wantDirty)
				}
			})
		}
	})
}

func TestCursorMovement(t *testing.T) {
	t.Run("Horizontal", func(t *testing.T) {
		tests := []struct {
			name       string
			rows       []string
			screenCols int
			cx         int
			cy         int
			colOff     int
			rowOff     int
			key        tcell.Key
			wantCX     int
			wantCY     int
			wantColOff int
			wantRowOff int
		}{
			{name: "move left within row", rows: []string{"abc"}, screenCols: 10, cx: 2, key: tcell.KeyLeft, wantCX: 1},
			{name: "move left with horizontal scroll", rows: []string{"abcdef"}, screenCols: 4, cx: 0, colOff: 2, key: tcell.KeyLeft, wantCX: 0, wantColOff: 1},
			{name: "move left at line start jumps to previous line", rows: []string{"abc", "de"}, screenCols: 10, cx: 0, cy: 1, key: tcell.KeyLeft, wantCX: 3, wantCY: 0},
			{name: "move right within row", rows: []string{"abc"}, screenCols: 10, cx: 1, key: tcell.KeyRight, wantCX: 2},
			{name: "move right at end of line moves to next row", rows: []string{"abc", "de"}, screenCols: 10, cx: 3, key: tcell.KeyRight, wantCX: 0, wantCY: 1},
			{name: "move right on long line scrolls horizontally", rows: []string{"abcdef"}, screenCols: 4, cx: 3, key: tcell.KeyRight, wantCX: 3, wantColOff: 1},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(tt.screenCols, 5, "", nil, tt.rows...)
				e.cx = tt.cx
				e.cy = tt.cy
				e.colOff = tt.colOff
				e.rowOff = tt.rowOff

				e.moveCursor(tt.key)

				if e.cx != tt.wantCX || e.cy != tt.wantCY || e.colOff != tt.wantColOff || e.rowOff != tt.wantRowOff {
					t.Fatalf("state = (cx=%d cy=%d colOff=%d rowOff=%d), want (%d %d %d %d)", e.cx, e.cy, e.colOff, e.rowOff, tt.wantCX, tt.wantCY, tt.wantColOff, tt.wantRowOff)
				}
			})
		}
	})

	t.Run("Vertical", func(t *testing.T) {
		tests := []struct {
			name       string
			rows       []string
			screenRows int
			cx         int
			cy         int
			colOff     int
			rowOff     int
			key        tcell.Key
			wantCX     int
			wantCY     int
			wantColOff int
			wantRowOff int
		}{
			{name: "move up within visible window", rows: []string{"a", "b", "c"}, screenRows: 4, cy: 2, key: tcell.KeyUp, wantCY: 1},
			{name: "move up at top scrolls upward", rows: []string{"a", "b", "c"}, screenRows: 4, cy: 0, rowOff: 2, key: tcell.KeyUp, wantRowOff: 1},
			{name: "move down within visible window", rows: []string{"a", "b", "c"}, screenRows: 4, cy: 0, key: tcell.KeyDown, wantCY: 1},
			{name: "move down at bottom scrolls", rows: []string{"a", "b", "c", "d"}, screenRows: 2, cy: 1, key: tcell.KeyDown, wantCY: 1, wantRowOff: 1},
			{name: "move down from last file row reaches eof row", rows: []string{"a"}, screenRows: 4, cy: 0, key: tcell.KeyDown, wantCY: 1},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(10, tt.screenRows, "", nil, tt.rows...)
				e.cx = tt.cx
				e.cy = tt.cy
				e.colOff = tt.colOff
				e.rowOff = tt.rowOff

				e.moveCursor(tt.key)

				if e.cx != tt.wantCX || e.cy != tt.wantCY || e.colOff != tt.wantColOff || e.rowOff != tt.wantRowOff {
					t.Fatalf("state = (cx=%d cy=%d colOff=%d rowOff=%d), want (%d %d %d %d)", e.cx, e.cy, e.colOff, e.rowOff, tt.wantCX, tt.wantCY, tt.wantColOff, tt.wantRowOff)
				}
			})
		}
	})

	t.Run("Clamp", func(t *testing.T) {
		tests := []struct {
			name       string
			rows       []string
			screenCols int
			screenRows int
			cx         int
			cy         int
			colOff     int
			rowOff     int
			key        tcell.Key
			wantCX     int
			wantCY     int
			wantColOff int
			wantRowOff int
		}{
			{name: "move from long row to shorter row clamps logical column", rows: []string{"abcdef", "xy"}, screenCols: 10, screenRows: 4, cx: 5, key: tcell.KeyDown, wantCX: 2, wantCY: 1},
			{name: "clamp adjusts cx", rows: []string{"abcd", "x"}, screenCols: 10, screenRows: 4, cx: 3, key: tcell.KeyDown, wantCX: 1, wantCY: 1},
			{name: "clamp adjusts colOff when cx would become negative", rows: []string{"abcdef", "x"}, screenCols: 4, screenRows: 4, cx: 0, colOff: 5, key: tcell.KeyDown, wantCX: 0, wantCY: 1, wantColOff: 1},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newTestEditor(tt.screenCols, tt.screenRows, "", nil, tt.rows...)
				e.cx = tt.cx
				e.cy = tt.cy
				e.colOff = tt.colOff
				e.rowOff = tt.rowOff

				e.moveCursor(tt.key)

				if e.cx != tt.wantCX || e.cy != tt.wantCY || e.colOff != tt.wantColOff || e.rowOff != tt.wantRowOff {
					t.Fatalf("state = (cx=%d cy=%d colOff=%d rowOff=%d), want (%d %d %d %d)", e.cx, e.cy, e.colOff, e.rowOff, tt.wantCX, tt.wantCY, tt.wantColOff, tt.wantRowOff)
				}
			})
		}
	})
}

func TestFileIO(t *testing.T) {
	t.Run("OpenFile", func(t *testing.T) {
		contentLF := "a\nb\n"
		contentCRLF := "a\r\nb\r\n"
		contentNoFinalNewline := "a\nb"

		tests := []struct {
			name      string
			content   *string
			filename  string
			wantRows  []string
			wantDirty bool
		}{
			{name: "missing file", filename: "missing.txt", wantDirty: false},
			{name: "lf file", filename: "lf.txt", content: &contentLF, wantRows: []string{"a", "b"}, wantDirty: false},
			{name: "crlf file", filename: "crlf.txt", content: &contentCRLF, wantRows: []string{"a", "b"}, wantDirty: false},
			{name: "without final newline", filename: "nonl.txt", content: &contentNoFinalNewline, wantRows: []string{"a", "b"}, wantDirty: false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				filename := filepath.Join(dir, tt.filename)
				if tt.content != nil {
					mustTempFile(t, dir, tt.filename, *tt.content)
				}

				e := newTestEditor(0, 0, "", nil)
				if err := e.openFile(filename); err != nil {
					t.Fatalf("openFile() error = %v", err)
				}

				if len(e.rows) != len(tt.wantRows) {
					t.Fatalf("row count = %d, want %d", len(e.rows), len(tt.wantRows))
				}
				for i, want := range tt.wantRows {
					got := rowText(e, i)
					if got != want {
						t.Fatalf("row %d = %q, want %q", i, got, want)
					}
					if strings.ContainsAny(got, "\r\n") {
						t.Fatalf("row %d contains newline characters: %q", i, got)
					}
				}
				if e.dirty != tt.wantDirty {
					t.Fatalf("dirty = %v, want %v", e.dirty, tt.wantDirty)
				}
			})
		}
	})

	t.Run("SaveFile", func(t *testing.T) {
		tests := []struct {
			name        string
			rows        []string
			wantContent string
		}{
			{name: "save one row", rows: []string{"a"}, wantContent: "a\n"},
			{name: "save multiple rows", rows: []string{"a", "b", "c"}, wantContent: "a\nb\nc\n"},
			{name: "save empty buffer", wantContent: ""},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				filename := filepath.Join(dir, "out.txt")
				e := newTestEditor(0, 0, filename, nil, tt.rows...)
				e.dirty = true

				e.saveFile()

				data, err := os.ReadFile(filename)
				if err != nil {
					t.Fatalf("ReadFile() error = %v", err)
				}
				if string(data) != tt.wantContent {
					t.Fatalf("file content = %q, want %q", string(data), tt.wantContent)
				}
				if e.dirty {
					t.Fatal("dirty = true, want false")
				}
				if !strings.Contains(e.statusMsg, "bytes written on disk") {
					t.Fatalf("statusMsg = %q, want substring %q", e.statusMsg, "bytes written on disk")
				}
			})
		}
	})
}

func newTestEditor(screenCols, screenRows int, filename string, syntax *editorSyntax, rows ...string) *editor {
	if screenCols == 0 {
		screenCols = 80
	}
	if screenRows == 0 {
		screenRows = 24
	}

	e := &editor{
		screenCols: screenCols,
		screenRows: screenRows,
		filename:   filename,
		syntax:     syntax,
		quitTimes:  kiloQuitTimes,
	}
	addRows(e, rows...)
	e.dirty = false
	return e
}

func addRows(e *editor, lines ...string) {
	for _, line := range lines {
		e.insertRow(len(e.rows), []byte(line))
	}
}

func hlSlice(row *erow, start, end int) []hlType {
	return row.hl[start:end]
}

func assertAllHL(t *testing.T, row *erow, start, end int, want hlType) {
	t.Helper()
	for i, got := range hlSlice(row, start, end) {
		if got != want {
			t.Fatalf("hl[%d] = %v, want %v", start+i, got, want)
		}
	}
}

func mustTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func rowText(e *editor, idx int) string {
	if idx < 0 || idx >= len(e.rows) {
		return ""
	}
	return string(e.rows[idx].chars)
}
