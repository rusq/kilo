package kilo

import "testing"

func TestTerminalText(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"plain\n\t猫 cafe\u0301 👨‍👩‍👧‍👦", "plain\n\t猫 cafe\u0301 👨‍👩‍👧‍👦"},
		{"\x00\x1b[31m\x7f\u0085", `\u0000\u001b[31m\u007f\u0085`},
		{"\u061c\u200e\u200f\u202a\u202e\u2066\u2069", `\u061c\u200e\u200f\u202a\u202e\u2066\u2069`},
		{string([]byte{'a', 0xff, 'b'}), "a\ufffdb"},
	} {
		if got := terminalText(tc.source); got != tc.want {
			t.Errorf("terminalText(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}
