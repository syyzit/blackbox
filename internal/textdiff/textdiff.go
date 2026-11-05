// Package textdiff renders a small line diff for human review, such as two versions
// of a tool definition. It favors clarity over speed and is meant for short texts.
package textdiff

import "strings"

// Op marks a diff line.
type Op byte

const (
	Same   Op = ' '
	Delete Op = '-'
	Insert Op = '+'
)

// Line is one line of a diff.
type Line struct {
	Op   Op
	Text string
}

// maxCells bounds the LCS table; beyond it the diff degrades to "all removed, all
// added", which is still correct, just less precise.
const maxCells = 4_000_000

// Lines diffs a against b by longest common subsequence.
func Lines(a, b []string) []Line {
	// Trim the common prefix and suffix first; definitions usually change a little.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}

	var out []Line
	for _, s := range a[:pre] {
		out = append(out, Line{Same, s})
	}
	out = append(out, middle(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, s := range a[len(a)-suf:] {
		out = append(out, Line{Same, s})
	}
	return out
}

func middle(a, b []string) []Line {
	var out []Line
	if (len(a)+1)*(len(b)+1) > maxCells {
		for _, s := range a {
			out = append(out, Line{Delete, s})
		}
		for _, s := range b {
			out = append(out, Line{Insert, s})
		}
		return out
	}

	// lcs[i][j] is the LCS length of a[i:] and b[j:].
	w := len(b) + 1
	lcs := make([]int, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, Line{Same, a[i]})
			i++
			j++
		case lcs[(i+1)*w+j] >= lcs[i*w+j+1]:
			out = append(out, Line{Delete, a[i]})
			i++
		default:
			out = append(out, Line{Insert, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, Line{Delete, a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, Line{Insert, b[j]})
	}
	return out
}

// Format renders a diff with the given number of unchanged context lines around each
// change; longer unchanged runs collapse to a "…" line.
func Format(lines []Line, context int) string {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op == Same {
			continue
		}
		for k := max(0, i-context); k <= min(len(lines)-1, i+context); k++ {
			keep[k] = true
		}
	}
	var b strings.Builder
	skipped := false
	for i, l := range lines {
		if !keep[i] {
			skipped = true
			continue
		}
		if skipped {
			b.WriteString("  …\n")
			skipped = false
		}
		b.WriteByte(byte(l.Op))
		b.WriteByte(' ')
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	if skipped {
		b.WriteString("  …\n")
	}
	return b.String()
}

// Changed reports whether a diff contains any insertion or deletion.
func Changed(lines []Line) bool {
	for _, l := range lines {
		if l.Op != Same {
			return true
		}
	}
	return false
}
