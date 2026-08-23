package cliui

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Indirections for testability.
var (
	Stdout = os.Stdout
	osExit = os.Exit
	comma  = func(n int64) string {
		// thousands separator without pulling a dependency
		s := fmt.Sprintf("%d", n)
		neg := strings.HasPrefix(s, "-")
		s = strings.TrimPrefix(s, "-")
		var parts []string
		for len(s) > 3 {
			parts = append([]string{s[len(s)-3:]}, parts...)
			s = s[:len(s)-3]
		}
		parts = append([]string{s}, parts...)
		out := strings.Join(parts, ",")
		if neg {
			out = "-" + out
		}
		return out
	}
	dur = func(d time.Duration) string {
		switch {
		case d >= time.Minute:
			return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
		case d >= time.Second:
			return fmt.Sprintf("%.0fs", d.Seconds())
		default:
			return fmt.Sprintf("%dms", d.Milliseconds())
		}
	}
)
