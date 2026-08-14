//go:build linux

package process

import "testing"

func TestParseLinuxStartTicksHandlesSpacesAndParentheses(t *testing.T) {
	t.Parallel()

	stat := "42 (ruk worker (test)) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 1800 20"
	ticks, err := parseLinuxStartTicks(stat)
	if err != nil {
		t.Fatalf("parseLinuxStartTicks returned an error: %v", err)
	}
	if ticks != 1800 {
		t.Fatalf("ticks = %d", ticks)
	}
}

func TestParseLinuxBootTime(t *testing.T) {
	t.Parallel()

	boot, err := parseLinuxBootTime("cpu 1 2 3\nintr 9\nbtime 1786740000\nprocesses 4\n")
	if err != nil {
		t.Fatalf("parseLinuxBootTime returned an error: %v", err)
	}
	if boot != 1786740000 {
		t.Fatalf("boot = %d", boot)
	}
}
