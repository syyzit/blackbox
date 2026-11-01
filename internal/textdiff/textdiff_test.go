package textdiff

import (
	"strings"
	"testing"
)

func TestLinesRoundTrips(t *testing.T) {
	cases := [][2]string{
		{"a b c", "a b c"},
		{"a b c", "a x c"},
		{"", "a b"},
		{"a b", ""},
		{"a b c d e", "b c e f"},
		{"x a y", "a"},
	}
	for _, c := range cases {
		a, b := strings.Fields(c[0]), strings.Fields(c[1])
		d := Lines(a, b)
		var gotA, gotB []string
		for _, l := range d {
			if l.Op != Insert {
				gotA = append(gotA, l.Text)
			}
			if l.Op != Delete {
				gotB = append(gotB, l.Text)
			}
		}
		if strings.Join(gotA, " ") != strings.Join(a, " ") || strings.Join(gotB, " ") != strings.Join(b, " ") {
			t.Errorf("%q -> %q: diff does not reproduce both sides: %+v", c[0], c[1], d)
		}
		if Changed(d) != (c[0] != c[1]) {
			t.Errorf("%q -> %q: Changed = %v", c[0], c[1], Changed(d))
		}
	}
}

func TestFormatCollapsesContext(t *testing.T) {
	a := strings.Fields("1 2 3 4 5 6 7 8 9")
	b := strings.Fields("1 2 3 4 X 6 7 8 9")
	got := Format(Lines(a, b), 1)
	want := "  …\n  4\n- 5\n+ X\n  6\n  …\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func FuzzLines(f *testing.F) {
	f.Add("a\nb\nc", "a\nc\nd")
	f.Add("", "x")
	f.Fuzz(func(t *testing.T, x, y string) {
		a, b := strings.Split(x, "\n"), strings.Split(y, "\n")
		var gotA, gotB []string
		for _, l := range Lines(a, b) {
			if l.Op != Insert {
				gotA = append(gotA, l.Text)
			}
			if l.Op != Delete {
				gotB = append(gotB, l.Text)
			}
		}
		if strings.Join(gotA, "\n") != x || strings.Join(gotB, "\n") != y {
			t.Fatalf("diff does not reproduce its inputs")
		}
	})
}
