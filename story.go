package barrage

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// StoryData is the plain-words verdict for a run: what happened, who caused
// it, and what to do next. Ported from the parked web UI's friendly report so
// the HTML report and JSON export tell the story without a browser app.
type StoryData struct {
	Title            string
	Tone             string // ok | err
	Detail           string
	Bottleneck       string
	TotalRequests    uint64
	Capacity         string // "" when the run can't support the model
	NextSteps        []string
	Throttled        []string // runners whose throughput lagged target
	LowVolume        bool
	MaskedSpikes     int
	CorrelatedSpikes int
}

// BuildStory derives the verdict from assembled report data. It is pure:
// same input, same story. Callers pass the finished ReportData (duration,
// ramp, concurrency set) — RenderHTML and BuildJSON do this themselves so
// every output carries the story with no ordering constraints.
func BuildStory(data ReportData) StoryData {
	if data.CapacitySearch != nil {
		return capacityStory(data.CapacitySearch)
	}
	if data.Error != "" {
		return StoryData{
			Title:  "Run failed before it could produce results",
			Tone:   "err",
			Detail: data.Error + " — check the target address / connection string, make sure the service is up, then try again.",
			NextSteps: []string{
				"Fix the connection, then re-run with --json and compare against this run to prove it.",
			},
		}
	}
	if len(data.Runners) == 0 {
		return StoryData{
			Title:  "No data — no runners returned results",
			Tone:   "err",
			Detail: "The run produced no requests. Check that at least one runner was enabled and targets were reachable.",
			NextSteps: []string{
				"Enable at least one runner (http, db, redis, scenario) and re-run.",
			},
		}
	}
	var total uint64
	for _, r := range data.Runners {
		total += r.Requests
	}
	if total == 0 {
		return StoryData{
			Title:  "Zero requests — targets unreachable?",
			Tone:   "err",
			Detail: "Runners were configured but sent nothing. Verify urls, db conn strings, and redis addr.",
			NextSteps: []string{
				"Verify the targets accept traffic (curl the url, ping the db), then re-run.",
			},
		}
	}
	s := StoryData{TotalRequests: total}
	worst := worstRunner(data.Runners)
	s.Bottleneck = worst.Name
	var broken, severe []RunnerSummary
	for _, r := range data.Runners {
		if r.Success < 99 {
			broken = append(broken, r)
		}
		if r.Success < 90 {
			severe = append(severe, r)
		}
		if r.Throughput > 0 && r.Rate > 0 && r.Throughput/r.Rate < 0.85 {
			s.Throttled = append(s.Throttled, r.Name)
		}
		if r.Requests < 20 {
			s.LowVolume = true
		}
	}
	for _, sp := range data.Spikes {
		if sp.Masked {
			s.MaskedSpikes++
		} else {
			s.CorrelatedSpikes++
		}
	}
	nSpikes := len(data.Spikes)

	switch {
	case nSpikes == 0 && len(broken) == 0:
		switch {
		case worst.P99 < 100*time.Millisecond:
			s.Title, s.Tone = "All good — fast and clean", "ok"
			s.Detail = fmt.Sprintf("Clean run for %s. Fast (p99 %s on %s) and 100%% success — nothing to fix.", data.Duration, worst.P99, worst.Name)
		case worst.P99 < 300*time.Millisecond:
			s.Title, s.Tone = "Healthy but a bit warm", "ok"
			s.Detail = fmt.Sprintf("No spikes or errors, but %s averaged p99 %s — watch it as load grows.", worst.Name, worst.P99)
		default:
			s.Title, s.Tone = fmt.Sprintf("Steady but slow — %s is warm", s.Bottleneck), "ok"
			s.Detail = fmt.Sprintf("No threshold spikes, yet %s p99 %s is heavy. Consider lowering the budget or tuning %s.", s.Bottleneck, worst.P99, s.Bottleneck)
		}
	case len(broken) > 0 && nSpikes == 0:
		if len(severe) > 0 {
			s.Title, s.Tone = joinNames(severe)+" is failing", "err"
			s.Detail = "Errors without latency spikes — fast failures (timeouts, refused, 4xx/5xx). " + pctList(severe) + ". Check status codes."
		} else {
			s.Title, s.Tone = joinNames(broken)+" had a few errors", "err"
			s.Detail = pctList(broken) + " — occasional failures under load, no clear latency bottleneck."
		}
	case nSpikes > 0 && len(broken) == 0:
		switch {
		case s.MaskedSpikes == nSpikes:
			s.Title, s.Tone = "Storage hiccup — app held", "ok"
			s.Detail = fmt.Sprintf("%d masked spike(s): %s spiked but http stayed under budget. App absorbed it — for now.", nSpikes, spikeRunners(data.Spikes))
		case s.CorrelatedSpikes > 0 && s.MaskedSpikes > 0:
			s.Title, s.Tone = fmt.Sprintf("Mixed — %s is the bottleneck", s.Bottleneck), "err"
			s.Detail = fmt.Sprintf("%d correlated + %d masked spike(s). %s p99 %s correlated with http — that's the bottleneck; masked ones are next.", s.CorrelatedSpikes, s.MaskedSpikes, s.Bottleneck, worst.P99)
		default:
			s.Title, s.Tone = fmt.Sprintf("Bottleneck: %s was the slowest", s.Bottleneck), "err"
			s.Detail = fmt.Sprintf("%d spike(s) crossed budget. %s p99 %s — that's where time went. Correlated with http in %d bucket(s).", nSpikes, s.Bottleneck, worst.P99, s.CorrelatedSpikes)
		}
	default:
		s.Title, s.Tone = fmt.Sprintf("Trouble on two fronts — %s slow and %s errored", s.Bottleneck, joinNames(broken)), "err"
		s.Detail = fmt.Sprintf("%d spike(s) and %d runner(s) with errors. Fix the bottleneck (%s p99 %s) and the failures (%s).", nSpikes, len(broken), s.Bottleneck, worst.P99, pctList(broken))
	}

	if s.LowVolume {
		s.NextSteps = append(s.NextSteps, "One or more runners had < 20 requests — percentiles are noisy. Increase duration or rate for tighter numbers.")
	}
	if len(s.Throttled) > 0 {
		s.NextSteps = append(s.NextSteps, fmt.Sprintf("Throughput lagged target on %s — raise concurrency, or the target is saturated (deliberately visible, not hidden).", strings.Join(s.Throttled, ", ")))
	}
	if len(severe) > 0 {
		s.NextSteps = append(s.NextSteps, fmt.Sprintf("Errors on %s are fast failures — usually wrong routes, refused connections, or 5xx responses. Check status codes in the report.", joinNames(severe)))
	} else if len(broken) > 0 {
		s.NextSteps = append(s.NextSteps, fmt.Sprintf("Occasional failures on %s only show up under load — look at timeouts and capacity, not correctness.", joinNames(broken)))
	}
	if s.CorrelatedSpikes > 0 {
		s.NextSteps = append(s.NextSteps, fmt.Sprintf("%s is dragging HTTP with it — indexes, connection-pool size, and query plans are the usual suspects.", s.Bottleneck))
	}
	if s.MaskedSpikes > 0 {
		s.NextSteps = append(s.NextSteps, fmt.Sprintf("%s spiked while HTTP stayed quiet — a bottleneck users can't feel yet. Tune it before they can.", spikeRunners(data.Spikes)))
	}
	if len(s.NextSteps) == 0 && worst.P99 >= 300*time.Millisecond {
		s.NextSteps = append(s.NextSteps, fmt.Sprintf("Nothing failed, but %s runs warm at p99 %s — set a tighter --fail-on budget and watch it across releases.", s.Bottleneck, worst.P99))
	}
	if len(s.NextSteps) == 0 {
		s.NextSteps = append(s.NextSteps, "Clean run — export this JSON and use it as your baseline for barrage compare.")
	}
	if len(data.ChaosEvents) > 0 {
		s.NextSteps = append(s.NextSteps, fmt.Sprintf("%d chaos fault event(s) ran during this test — spikes inside a fault window are the fault's blast radius, not a new bottleneck. Check the chaos faults table against the timeline.", len(data.ChaosEvents)))
	}

	s.Capacity = capacityLine(data)
	return s
}

// capacityStory verdicts a capacity sweep: where it broke and who did it.
func capacityStory(res *CapacityResult) StoryData {
	if res == nil || len(res.Steps) == 0 {
		return StoryData{Title: "Sweep produced no levels", Tone: "err", Detail: "No concurrency level ran. Check the capacity config and target reachability."}
	}
	causes := map[string]bool{}
	var total uint64
	for _, s := range res.Steps {
		total += s.Requests
		for _, c := range s.BrokenBy {
			causes[c] = true
		}
	}
	causeList := make([]string, 0, len(causes))
	for c := range causes {
		causeList = append(causeList, c)
	}
	sort.Strings(causeList)
	if res.BreakAt != 0 {
		detail := fmt.Sprintf("Held to %d concurrent users, broke at %d.", res.LastOK, res.BreakAt)
		if len(causeList) > 0 {
			detail += fmt.Sprintf(" Blame: %s.", strings.Join(causeList, ", "))
		}
		return StoryData{
			Title:         fmt.Sprintf("Broke at %d concurrent users", res.BreakAt),
			Tone:          "err",
			Detail:        detail + " See the capacity chart for cliff vs slope.",
			TotalRequests: total,
			NextSteps: []string{
				fmt.Sprintf("Re-test at %d users after the fix to prove the knee moved.", res.BreakAt),
				"Same-machine numbers are relative — generate load from a separate box for real capacity.",
			},
		}
	}
	return StoryData{
		Title:         fmt.Sprintf("Held to %d concurrent users", res.LastOK),
		Tone:          "ok",
		Detail:        fmt.Sprintf("No level broke up to the %d cap. Raise max_concurrency to find the real knee.", res.LastOK),
		TotalRequests: total,
		NextSteps: []string{
			"Clean search — save this JSON and compare after infra changes.",
		},
	}
}

func worstRunner(rs []RunnerSummary) RunnerSummary {
	worst := rs[0]
	for _, r := range rs[1:] {
		if r.P99 > worst.P99 {
			worst = r
		}
	}
	return worst
}

func joinNames(rs []RunnerSummary) string {
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}

func pctList(rs []RunnerSummary) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, fmt.Sprintf("%s %.1f%%", r.Name, r.Success))
	}
	return strings.Join(parts, ", ")
}

func spikeRunners(spikes []CorrelatedSpike) string {
	seen := map[string]bool{}
	var out []string
	for _, s := range spikes {
		if !seen[s.Runner] {
			seen[s.Runner] = true
			out = append(out, titleName(s.Runner))
		}
	}
	return strings.Join(out, ", ")
}

// capacityLine estimates where strain starts: map each bucket to active
// users (linear during ramp, flat after), then find the first 3 consecutive
// buckets where the worst journey P99 exceeds 2x its median. Empty when the
// run can't support the model (no journey series, too few buckets).
func capacityLine(data ReportData) string {
	conc := data.Concurrency
	if conc <= 0 {
		return ""
	}
	series := journeySeries(data.Timeline)
	n := len(data.Timeline.Labels)
	if len(series) == 0 || n < 10 {
		return ""
	}
	perBucket := make([]int64, n)
	for i := 0; i < n; i++ {
		worst := int64(-1)
		for _, s := range series {
			if i < len(s.P99) && int64(s.P99[i]) > worst {
				worst = int64(s.P99[i])
			}
		}
		perBucket[i] = worst
	}
	var valid []int64
	for _, v := range perBucket {
		if v >= 0 {
			valid = append(valid, v)
		}
	}
	if len(valid) < 5 {
		return ""
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i] < valid[j] })
	median := valid[len(valid)/2]
	strainLevel := median * 2
	if median+50 > strainLevel {
		strainLevel = median + 50
	}
	rampSec := 0.0
	if d, err := time.ParseDuration(data.Ramp); err == nil {
		rampSec = d.Seconds()
	}
	usersAt := func(i int) int {
		if rampSec <= 0 {
			return conc
		}
		u := float64(conc) * float64(i+1) / rampSec
		if u < 1 {
			u = 1
		}
		if u > float64(conc) {
			u = float64(conc)
		}
		return int(u + 0.5)
	}
	knee := -1
	for i := 0; i+2 < n; i++ {
		if perBucket[i] >= 0 && perBucket[i+1] >= 0 && perBucket[i+2] >= 0 &&
			perBucket[i] >= strainLevel && perBucket[i+1] >= strainLevel && perBucket[i+2] >= strainLevel {
			knee = i
			break
		}
	}
	if knee >= 0 {
		return fmt.Sprintf("straining at ~%d users (%s) — worst journey p99 over 2x its %dms median for 3+ buckets", usersAt(knee), data.Timeline.Labels[knee], median)
	}
	return fmt.Sprintf("no strain up to ~%d users", conc)
}

// journeySeries returns timeline series that are user-facing scenarios,
// not the http/db/redis infrastructure runners.
func journeySeries(t TimelineChart) []TimelineSeries {
	var out []TimelineSeries
	for _, s := range t.Series {
		switch strings.ToLower(s.Name) {
		case "http", "db", "redis":
			continue
		}
		out = append(out, s)
	}
	return out
}
