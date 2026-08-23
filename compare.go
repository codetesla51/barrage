package barrage

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"os"
	"sort"
)

//go:embed templates/compare.html
var compareReportTemplate string

// Comparison is one runner's latency delta between a baseline run and a current
// run. CurrentP99 is the value being judged; Regressed reports whether it
// crossed the budget passed to it. New marks runners absent from the baseline —
// they can't regress, they're just new.
type Comparison struct {
	Name        string
	BaselineP99 int64
	CurrentP99  int64
	PctChange   int  // rounded (current-baseline)/baseline*100; 0 if baseline is 0
	New         bool // runner not present in the baseline run
}

// Regressed reports whether this runner's current P99 crossed the budget in
// milliseconds, compared against the baseline: it regresses when current is
// above the budget AND the baseline was at or under it. Brand-new runners are
// never regressions.
func (c Comparison) Regressed(budgetMS int64) bool {
	if c.New {
		return false
	}
	return c.CurrentP99 > budgetMS && c.BaselineP99 <= budgetMS
}

// CompareRun diffs two JSON reports runner by runner, matched by Name. Runners
// present in only one report are included with a zero latency on the missing
// side. Rows are sorted by name.
func CompareRun(baseline, current *JSONReport) []Comparison {
	if baseline == nil {
		baseline = &JSONReport{}
	}
	if current == nil {
		current = &JSONReport{}
	}

	byName := make(map[string]JSONRunner, len(baseline.Runners))
	for _, r := range baseline.Runners {
		byName[r.Name] = r
	}

	seen := make(map[string]bool, len(current.Runners))
	var rows []Comparison
	for _, c := range current.Runners {
		seen[c.Name] = true
		b, ok := byName[c.Name]
		row := newComparison(c.Name, b.P99MS, c.P99MS)
		row.New = !ok
		rows = append(rows, row)
	}
	for _, b := range baseline.Runners {
		if !seen[b.Name] {
			rows = append(rows, newComparison(b.Name, b.P99MS, 0))
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func newComparison(name string, baselineP99, currentP99 int64) Comparison {
	var pct int
	if baselineP99 > 0 {
		pct = int((currentP99 - baselineP99) * 100 / baselineP99)
	}
	return Comparison{Name: name, BaselineP99: baselineP99, CurrentP99: currentP99, PctChange: pct}
}

// SpikeComparison records one spike bucket's change between a baseline run and
// a current run, keyed by runner and bucket time. Status is one of: new (spike
// appeared in current), fixed (spike gone from current), worsened (still
// spiking, slower), improved (still spiking, faster), or unchanged.
type SpikeComparison struct {
	BucketTime      string
	Runner          string
	BaselineHTTPMS  int64
	BaselineStoreMS int64
	CurrentHTTPMS   int64
	CurrentStoreMS  int64
	Status          string
}

// CompareSpikes diffs the correlated-spike lists of two JSON reports, matched
// by (runner, bucket time). Spikes present in only one side are reported as
// new or fixed; spikes present on both sides are classified by how the storage
// P99 moved. Rows are sorted by bucket time then runner.
func CompareSpikes(baseline, current *JSONReport) []SpikeComparison {
	if baseline == nil {
		baseline = &JSONReport{}
	}
	if current == nil {
		current = &JSONReport{}
	}

	// match spikes by ordinal position per runner — two runs happen at
	// different times of day, so absolute wall-clock keys would make every
	// spike look "new". The 1st db spike of this run compares against the
	// 1st db spike of the baseline, and so on.
	byRunner := func(rep *JSONReport) map[string][]JSONSpike {
		m := make(map[string][]JSONSpike)
		for _, sp := range rep.Spikes {
			m[sp.Runner] = append(m[sp.Runner], sp)
		}
		return m
	}
	baseBy, curBy := byRunner(baseline), byRunner(current)

	runners := make([]string, 0, len(curBy))
	for r := range curBy {
		runners = append(runners, r)
	}
	sort.Strings(runners)

	var rows []SpikeComparison
	for _, runner := range runners {
		baseList, curList := baseBy[runner], curBy[runner]
		n := len(baseList)
		if len(curList) > n {
			n = len(curList)
		}
		for i := 0; i < n; i++ {
			switch {
			case i >= len(baseList):
				rows = append(rows, SpikeComparison{
					BucketTime: curList[i].BucketTime, Runner: runner,
					CurrentHTTPMS: curList[i].HTTPP99MS, CurrentStoreMS: curList[i].StorageP99MS,
					Status: "new",
				})
			case i >= len(curList):
				rows = append(rows, SpikeComparison{
					BucketTime: baseList[i].BucketTime, Runner: runner,
					BaselineHTTPMS: baseList[i].HTTPP99MS, BaselineStoreMS: baseList[i].StorageP99MS,
					Status: "fixed",
				})
			default:
				c, b := curList[i], baseList[i]
				status := "unchanged"
				switch {
				case c.StorageP99MS > b.StorageP99MS:
					status = "worsened"
				case c.StorageP99MS < b.StorageP99MS:
					status = "improved"
				}
				rows = append(rows, SpikeComparison{
					BucketTime: c.BucketTime, Runner: runner,
					BaselineHTTPMS: b.HTTPP99MS, BaselineStoreMS: b.StorageP99MS,
					CurrentHTTPMS: c.HTTPP99MS, CurrentStoreMS: c.StorageP99MS,
					Status: status,
				})
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].BucketTime != rows[j].BucketTime {
			return rows[i].BucketTime < rows[j].BucketTime
		}
		return rows[i].Runner < rows[j].Runner
	})
	return rows
}

// CompareTimelineSeries is one runner's per-bucket P99 across both runs,
// aligned to CompareTimeline.Labels. -1 marks a bucket where that run had no
// request (a gap in the chart).
type CompareTimelineSeries struct {
	Name     string
	Baseline []int // -1 = no request in that bucket
	Current  []int // -1 = no request in that bucket
}

// CompareTimeline holds both runs' per-bucket P99 lines on a shared label axis.
type CompareTimeline struct {
	Labels []string
	Series []CompareTimelineSeries
}

// BuildCompareTimeline aligns the baseline and current timelines onto one set
// of bucket labels (a union of both runs' buckets). Each runner appears once
// with a baseline line and a current line.
func BuildCompareTimeline(baseline, current *JSONReport) CompareTimeline {
	var chart CompareTimeline
	if baseline == nil {
		baseline = &JSONReport{}
	}
	if current == nil {
		current = &JSONReport{}
	}

	indexSet := make(map[string]bool)
	names := make(map[string]bool)
	points := make(map[string]*[2]map[string]int) // runner -> {baseline, current}
	get := func(name string) *[2]map[string]int {
		if points[name] == nil {
			points[name] = &[2]map[string]int{{}, {}}
		}
		return points[name]
	}
	have := func(which int, labels []string, series []JSONTimelineSeries) {
		for _, s := range series {
			names[s.Name] = true
			m := get(s.Name)[which]
			for i, l := range labels {
				if s.P99[i] >= 0 {
					m[l] = s.P99[i]
				}
				indexSet[l] = true
			}
		}
	}
	have(0, baseline.Timeline.Labels, baseline.Timeline.Series)
	have(1, current.Timeline.Labels, current.Timeline.Series)

	labels := make([]string, 0, len(indexSet))
	for l := range indexSet {
		labels = append(labels, l)
	}
	sort.Strings(labels)

	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)

	chart.Labels = labels
	for _, n := range ordered {
		bl, cur := make([]int, len(labels)), make([]int, len(labels))
		for i, l := range labels {
			if v, ok := points[n][0][l]; ok {
				bl[i] = v
			} else {
				bl[i] = -1
			}
			if v, ok := points[n][1][l]; ok {
				cur[i] = v
			} else {
				cur[i] = -1
			}
		}
		chart.Series = append(chart.Series, CompareTimelineSeries{Name: n, Baseline: bl, Current: cur})
	}
	return chart
}

// CompareReportData is the data model for the HTML comparison report: the
// per-runner diff, the spike diff, and both runs' timelines overlaid.
type CompareReportData struct {
	BaselineName string
	CurrentName  string
	FailOn       string
	FailOnMS     int64
	Runners      []Comparison
	Spikes       []SpikeComparison
	Timeline     CompareTimeline
}

// NewCompareReportData assembles a CompareReportData from two JSON reports.
func NewCompareReportData(baseline, current *JSONReport, failOnMS int64) CompareReportData {
	return CompareReportData{
		Runners:  CompareRun(baseline, current),
		Spikes:   CompareSpikes(baseline, current),
		Timeline: BuildCompareTimeline(baseline, current),
		FailOn:   fmt.Sprintf("%dms", failOnMS),
		FailOnMS: failOnMS,
	}
}

// RenderCompare renders the HTML comparison report to w. Like RenderHTML, a
// template file at templatePath overrides the embedded one.
func RenderCompare(data CompareReportData, templatePath string, w io.Writer) error {
	tmplSrc, err := os.ReadFile(templatePath)
	if err != nil {
		tmplSrc = []byte(compareReportTemplate)
	}
	tmpl, err := template.New("compare").Funcs(template.FuncMap{
		"timelineColor": timelineColor,
		"countBad":      countBad,
		"countStatus":   countStatus,
	}).Parse(string(tmplSrc))
	if err != nil {
		return fmt.Errorf("parsing compare template: %w", err)
	}
	return tmpl.Execute(w, data)
}

// countBad returns how many runner comparisons regressed against the budget.
func countBad(rows []Comparison, budgetMS int64) int {
	var n int
	for _, r := range rows {
		if r.Regressed(budgetMS) {
			n++
		}
	}
	return n
}

// countStatus returns how many spike comparisons have the given status.
func countStatus(spikes []SpikeComparison, status string) int {
	var n int
	for _, s := range spikes {
		if s.Status == status {
			n++
		}
	}
	return n
}
