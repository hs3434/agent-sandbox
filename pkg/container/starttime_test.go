package container

import (
	"strings"
	"testing"
)

// T7 golden tests (D-05/D-20): the /proc/<pid>/stat parser must split AFTER
// the last ')' — comm may contain spaces, parens, digits (adversarial names).
func TestStatParserGolden(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		want     string
		note     string
	}{
		{"simple", "123 (sleep) S 1 123 123 0 -1 4194560 100 0 0 0 10 5 0 0 20 0 1 0 12345 5000 100 18446744073709551615 1 1 1 0 0 0 0 16781312 0 0 0 0 17 0 0 0 0 0 0 0 0 0 0 0 0 0", "12345", "comm=sleep"},
		{"parens in comm", "123 (bad (name)) S 1 123 123 0 -1 4194560 100 0 0 0 10 5 0 0 20 0 1 0 98765 5000 100 0 1 1 1 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0 0 0 0 0 0 0 0", "98765", "split at LAST )"},
		{"spaces in comm", "123 (my proc v2) S 1 123 123 0 -1 4194560 100 0 0 0 10 5 0 0 20 0 1 0 42 5000 100 0 1 1 1 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0 0 0 0 0 0 0 0", "42", "spaces survive"},
		{"zombie", "99 (defunct) Z 1 99 99 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 777 0 0 0 1 1 1 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0 0 0 0 0 0 0 0", "777", "zombies still parse"},
	}
	for _, c := range cases {
		if got := starttimeFromStat(c.line); got != c.want {
			t.Errorf("%s: starttimeFromStat = %q, want %q (%s)", c.name, got, c.want, c.note)
		}
	}
	// garbage produces empty (never a wrong-but-plausible number)
	if got := starttimeFromStat("no parens here"); got != "" {
		t.Errorf("garbage line = %q, want empty", got)
	}
}

func TestTerminalFromExitOut(t *testing.T) {
	cases := []struct{ in, want string }{
		{"7", "exited(7)"},
		{"0", "exited(0)"},
		{"sig:9", "signaled(9)"},
		{"", ""},
		{"__NOEXIT__", ""},
		{"garbage", "exit_unknown"},
	}
	for _, c := range cases {
		if got := terminalFromExitOut(c.in); got != c.want {
			t.Errorf("terminalFromExitOut(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// starttimeFromStat is the parser extracted for golden testing (used via
// readStarttimeFallback's logic).
func starttimeFromStat(line string) string {
	idx := strings.LastIndex(line, ")")
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(line[idx+1:])
	if len(fields) >= 20 {
		return fields[19]
	}
	return ""
}
