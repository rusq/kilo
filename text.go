package kilo

import (
	"fmt"
	"strings"
	"unicode"
)

// terminalText projects unsafe characters for display without rewriting source.
// Newline and tab remain available to the editor's layout and find handling.
func terminalText(s string) string {
	var out strings.Builder
	for _, r := range s {
		bidi := r == 0x061c || r == 0x200e || r == 0x200f ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || bidi {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
