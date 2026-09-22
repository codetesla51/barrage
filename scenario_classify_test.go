package barrage

import (
	"errors"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"
)

// timeoutErr lets tests fabricate network timeouts: net.OpError reads the
// net.Error interface when deciding Timeout().
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func dialRefused() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}

func dialTimeout() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}
}

func readTimeout() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}
}

func connReset() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
}

func TestClassifyStep(t *testing.T) {
	cases := []struct {
		name string
		step StepResult
		want string
	}{
		{"refused dial", StepResult{Err: dialRefused()}, "connection_refused"},
		{"refused dial wrapped by client", StepResult{Err: &url.Error{Op: "Post", URL: "http://demoserver/api/login", Err: dialRefused()}}, "connection_refused"},
		{"dial timeout", StepResult{Err: dialTimeout()}, "dial_timeout"},
		{"read timeout", StepResult{Err: readTimeout()}, "read_timeout"},
		{"conn reset", StepResult{Err: connReset()}, "conn_reset"},
		{"plain error", StepResult{Err: errors.New("boom")}, "transport"},
		{"other op error", StepResult{Err: &net.OpError{Op: "read", Err: syscall.EPIPE}}, "transport"},
		{"dns timeout", StepResult{Err: &net.DNSError{Err: "no such host", IsTimeout: true}}, "timeout"},
		{"server error", StepResult{StatusCode: 500}, "5xx"},
		{"gateway error", StepResult{StatusCode: 503}, "5xx"},
		{"client error", StepResult{StatusCode: 404}, "4xx"},
		{"too many requests", StepResult{StatusCode: 429}, "4xx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStep(tc.step); got != tc.want {
				t.Errorf("classifyStep = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestScenarioStatsErrCounts proves every failing step is bucketed (not just
// the first per iteration), successes count nothing, and the readable Error
// sample still fills.
func TestScenarioStatsErrCounts(t *testing.T) {
	start := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	results := []ScenarioResult{
		{Start: start, Duration: time.Millisecond, Steps: []StepResult{{StatusCode: 200}}},
		{Start: start, Duration: time.Millisecond, Steps: []StepResult{{StatusCode: 200}, {StatusCode: 503}}},
		{Start: start, Duration: time.Millisecond, Steps: []StepResult{{Err: dialRefused()}}},
		{Start: start, Duration: time.Millisecond, Steps: []StepResult{{StatusCode: 500}, {StatusCode: 500}}},
	}
	stats := buildScenarioStats(results, start, time.Second, time.Second)

	if stats.Requests != 4 {
		t.Errorf("requests = %d, want 4", stats.Requests)
	}
	if stats.Success != 0.25 {
		t.Errorf("success = %v, want 0.25", stats.Success)
	}
	want := map[string]uint64{"5xx": 3, "connection_refused": 1}
	if len(stats.ErrCounts) != len(want) {
		t.Errorf("ErrCounts = %v, want %v", stats.ErrCounts, want)
	}
	for class, n := range want {
		if stats.ErrCounts[class] != n {
			t.Errorf("ErrCounts[%q] = %d, want %d", class, stats.ErrCounts[class], n)
		}
	}
	if len(stats.Errors) == 0 {
		t.Error("expected at least one readable error sample")
	}
}

// TestScenarioStatsNoErrorsLeavesErrCountsNil keeps the export's omitempty
// honest: a clean level carries no errors map at all.
func TestScenarioStatsNoErrorsLeavesErrCountsNil(t *testing.T) {
	start := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	results := []ScenarioResult{{Start: start, Duration: time.Millisecond, Steps: []StepResult{{StatusCode: 200}}}}
	stats := buildScenarioStats(results, start, time.Second, time.Second)
	if stats.ErrCounts != nil {
		t.Errorf("ErrCounts = %v, want nil", stats.ErrCounts)
	}
}
