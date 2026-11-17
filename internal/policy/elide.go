package policy

import (
	"fmt"
	"unicode/utf8"
)

// Elide shortens text for display by cutting out its middle and saying how much is
// missing. Both ends stay visible: text that is merely cut off at the end lets a long
// harmless argument push the one that matters out of sight.
func Elide(s string, head, tail int) string {
	if len(s) <= head+tail {
		return s
	}
	// Do not split a multi-byte character at either cut.
	h, t := head, len(s)-tail
	for h > 0 && !utf8.RuneStart(s[h]) {
		h--
	}
	for t < len(s) && !utf8.RuneStart(s[t]) {
		t++
	}
	return fmt.Sprintf("%s … [%d bytes not shown] … %s", s[:h], t-h, s[t:])
}
