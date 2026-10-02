package tui

import "testing"

func TestTruncateDebugTmp(t *testing.T) {
	for _, s := range []string{"abcdef", "运行中"} {
		for _, w := range []int{1, 2, 3, 4, 5, 6} {
			got := Truncate(s, w)
			t.Logf("%q width=%d -> %q (measure=%d, rawlen=%d)", s, w, got, Measure(got), len(got))
		}
	}
}
