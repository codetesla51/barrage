package cliui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/codetesla51/barrage"
	"github.com/muesli/termenv"
)

func TestInteractiveMatrix(t *testing.T) {
	cases := []struct {
		name              string
		requested, json   bool
		tty               bool
		env               map[string]string
		want              bool
	}{
		{"requested+tty", true, false, true, map[string]string{}, true},
		{"no flag", false, false, true, map[string]string{}, false},
		{"json output", true, true, true, map[string]string{}, false},
		{"piped stdout", true, false, false, map[string]string{}, false},
		{"ci env", true, false, true, map[string]string{"CI": "true"}, false},
		{"github actions", true, false, true, map[string]string{"GITHUB_ACTIONS": "true"}, false},
		{"dumb term", true, false, true, map[string]string{"TERM": "dumb"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Interactive(tc.requested, tc.json, tc.tty, tc.env)
			if got != tc.want {
				t.Fatalf("Interactive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSuccessColorizePlainValues(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii) // force plain profile for this test
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.ANSI256) })
	if got := SuccessColorize(99.98); got != "100.0%" { // rounds to 100.0 and must be unstyled
		t.Fatalf("healthy success should be unstyled, got %q", got)
	}

}

func TestSuccessColorizeStyledWhenColorAvailable(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.ANSI256) })
	if got := SuccessColorize(50.0); !strings.Contains(got, "50.0%") {
		t.Fatalf("expected value in output, got %q", got)
	}
}

type fakeProgram struct{ model teaModel }

// smoke: the progress view renders runner rows from real RunProgress data.
func TestProgressViewRendersRows(t *testing.T) {
	prog := barrage.NewRunProgress(60*time.Second, 0, 4)
	prog.Record("http", true, 12*time.Millisecond)
	prog.Record("db", false, 90*time.Millisecond)

	m := newProgressModel(prog, make(chan error, 1), 40)
	out := m.View()
	for _, want := range []string{"running", "http", "db", "reqs", "p99"} {
		if !strings.Contains(out, want) {
			t.Fatalf("view missing %q:\n%s", want, out)
		}
	}
	_ = fakeProgram{} // keep type if unused later
}

type teaModel = interface{} // alias to avoid importing tea in this test file
