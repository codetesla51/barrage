package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/codetesla51/barrage"
	"github.com/codetesla51/barrage/internal/cliui"
	"github.com/codetesla51/barrage/internal/version"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	_ "modernc.org/sqlite"
)

const banner = `     ________  ________  ________  ________  ________  ________  _______
    |\   __  \|\   __  \|\   __  \|\   __  \|\   __  \|\   ____\|\  ___ \
    \ \  \|\ /\ \  \|\  \ \  \|\  \ \  \|\  \ \  \|\  \ \  \___|\ \   __/|
     \ \   __  \ \   __  \ \   _  _\ \   _  _\ \   __  \ \  \  __\ \  \_|/__
      \ \  \|\  \ \  \ \  \ \  \\  \\ \  \\  \\ \  \ \  \ \  \|\  \ \  \_|\ \
       \ \_______\ \__\ \__\ \__\\ _\\ \__\\ _\\ \__\ \__\ \_______\ \_______\
        \|_______|\|__|\|__|\|__|\|__|\|__|\|__|\|__|\|__|\|_______|\|_______|`

type runOptions struct {
	config            string
	report            string
	noReport          bool
	open              bool
	duration          time.Duration
	bucketWidth       time.Duration
	ramp              time.Duration
	concurrency       int
	jsonPath          string
	httpThreshold     time.Duration
	dbThreshold       time.Duration
	redisThreshold    time.Duration
	verbose           bool
	noProgress        bool
	capacity          bool
	capacityMaxConcur int
	capacityStepDur   time.Duration
}

type compareOptions struct {
	baseline string
	current  string
	failOn   time.Duration
	report   string
	open     bool
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "barrage",
		Short: "Barrage is a multi-target load testing tool",
		Long: banner + "\n\nBarrage runs concurrent load tests against HTTP, database, and Redis\n" +
			"targets, correlates latency spikes between them, and renders an HTML report.\n\n" +
			"Run `barrage run --help` to see the load-testing options.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCmd(), newCompareCmd(), newVersionCmd())
	return root
}

func newRunCmd() *cobra.Command {
	opts := &runOptions{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a load test from a config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			warnDeprecatedRunFlags(cmd)
			return runLoadTest(opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.config, "config", "c", "config.yaml", "path to the config file")
	f.StringVar(&opts.report, "report", "report.html", "path for the HTML report")
	f.BoolVar(&opts.noReport, "no-report", false, "skip writing the HTML report")
	f.BoolVarP(&opts.open, "open", "o", false, "open the report in a browser after the run")
	f.DurationVarP(&opts.duration, "duration", "d", 0, "override the run duration from the config")
	f.DurationVarP(&opts.bucketWidth, "bucket-width", "b", 0, "override the bucket width from the config")
	f.DurationVar(&opts.ramp, "ramp", 0, "ramp the rate from 0 up to full over this duration")
	f.IntVar(&opts.concurrency, "concurrency", 0, "worker count for the db/redis pools and http attackers")
	f.StringVar(&opts.jsonPath, "json", "", "also write a JSON summary of the run to this path")
	f.DurationVar(&opts.httpThreshold, "http-threshold", 100*time.Millisecond, "HTTP spike threshold for correlation")
	f.DurationVar(&opts.dbThreshold, "db-threshold", 100*time.Millisecond, "DB spike threshold for correlation")
	f.DurationVar(&opts.redisThreshold, "redis-threshold", 100*time.Millisecond, "Redis spike threshold for correlation")
	f.BoolVarP(&opts.verbose, "verbose", "v", false, "print per-bucket detail")
	f.BoolVar(&opts.noProgress, "no-progress", false, "disable the live progress view (plain log lines instead)")
	f.BoolVar(&opts.capacity, "capacity", false, "sweep concurrency (double, then fine fill) to find the break point")
	f.IntVar(&opts.capacityMaxConcur, "capacity-max-concurrency", 0, "cap for the capacity sweep")
	f.DurationVar(&opts.capacityStepDur, "capacity-step-duration", 0, "per-level burst time for the capacity sweep (default 10s)")
	// Pre-0.6 names, kept as hidden aliases so existing invocations keep working.
	f.BoolVar(&opts.capacity, "auto-ramp", false, "deprecated, use --capacity")
	f.IntVar(&opts.capacityMaxConcur, "ramp-max-concurrency", 0, "deprecated, use --capacity-max-concurrency")
	f.DurationVar(&opts.capacityStepDur, "ramp-step-duration", 0, "deprecated, use --capacity-step-duration")
	_ = cmd.Flags().MarkHidden("auto-ramp")
	_ = cmd.Flags().MarkHidden("ramp-max-concurrency")
	_ = cmd.Flags().MarkHidden("ramp-step-duration")
	return cmd
}

func newCompareCmd() *cobra.Command {
	opts := &compareOptions{}
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare a baseline run against a current run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompare(opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.baseline, "baseline", "", "path to the baseline JSON report")
	f.StringVar(&opts.current, "current", "", "path to the current JSON report")
	f.DurationVar(&opts.failOn, "fail-on", 100*time.Millisecond, "fail (exit non-zero) if a runner regresses above this latency budget")
	f.StringVar(&opts.report, "report", "compare.html", "path for the HTML comparison report, or empty to skip it")
	f.BoolVarP(&opts.open, "open", "o", false, "open the report in a browser after comparing")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("barrage %s\n", version.Version)
		},
	}
}

func runLoadTest(opts *runOptions) error {
	if opts.open && opts.noReport {
		return errors.New("--open cannot be used with --no-report")
	}

	cfg, err := barrage.LoadConfig(opts.config)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if opts.duration > 0 {
		cfg.Duration = barrage.Duration(opts.duration)
	}
	if opts.bucketWidth > 0 {
		cfg.BucketWidth = barrage.Duration(opts.bucketWidth)
	}
	if opts.ramp > 0 {
		cfg.Ramp = barrage.Duration(opts.ramp)
	}
	if opts.concurrency > 0 {
		cfg.Concurrency = opts.concurrency
	}

	if opts.capacity && cfg.Capacity == nil {
		cfg.Capacity = &barrage.CapacityConfig{}
	}
	if opts.capacityMaxConcur > 0 {
		if cfg.Capacity == nil {
			cfg.Capacity = &barrage.CapacityConfig{}
		}
		cfg.Capacity.MaxConcurrency = opts.capacityMaxConcur
	}
	if opts.capacityStepDur > 0 {
		if cfg.Capacity == nil {
			cfg.Capacity = &barrage.CapacityConfig{}
		}
		cfg.Capacity.StepDuration = barrage.Duration(opts.capacityStepDur)
	}
	if cfg.UsedDeprecatedAutoRamp {
		fmt.Fprintln(os.Stderr, "warning: auto_ramp: is deprecated; rename it to capacity:")
	}

	fmt.Println(banner)
	fmt.Println()
	fmt.Printf("barrage %s\n", version.Version)
	if cfg.Capacity != nil {
		stepDur := time.Duration(cfg.Capacity.StepDuration)
		if stepDur <= 0 {
			stepDur = 10 * time.Second
		}
		fmt.Println(cliui.Dim(fmt.Sprintf("capacity %d → %d · step %s · bucket %s",
			effectiveConcurrency(cfg), cfg.Capacity.MaxConcurrency, stepDur, time.Duration(cfg.BucketWidth))))
	} else {
		fmt.Println(cliui.Dim(fmt.Sprintf("duration %s · bucket %s · concurrency %d · ramp %s",
			time.Duration(cfg.Duration), time.Duration(cfg.BucketWidth), effectiveConcurrency(cfg), time.Duration(cfg.Ramp))))
	}
	if rates := configuredRates(cfg); len(rates) > 0 {
		fmt.Println(cliui.Dim("rates    " + strings.Join(rates, " · ")))
	}

	fmt.Println()

	if cfg.Capacity != nil {
		return runCapacitySweep(opts, cfg)
	}

	// Live progress: Bubble Tea view on TTY stderr, plain 5s log lines
	// otherwise (pipes, CI). Stderr keeps piped stdout clean either way.
	stats := &barrage.RunStats{}
	cfg.Stats = stats
	var prog *tea.Program
	if !opts.noProgress && term.IsTerminal(int(os.Stderr.Fd())) {
		cfg.Quiet = true
		prog = tea.NewProgram(
			barrage.NewProgressModel(stats, time.Duration(cfg.Duration)),
			tea.WithOutput(os.Stderr),
		)
		go func() { _, _ = prog.Run() }()
	}

	result, err := barrage.Orchestrator(*cfg)
	if prog != nil {
		prog.Quit()
		prog.Wait()
	}
	if err != nil {
		return fmt.Errorf("load test failed: %w", err)
	}

	printResults(result, opts.verbose)

	var spikes barrage.CorrelationResult
	if result.DBResult != nil || result.RedisResult != nil {
		spikes = barrage.Correlate(result, opts.httpThreshold, opts.dbThreshold, opts.redisThreshold)
		printSpikes(spikes, opts.httpThreshold)
	}

	if !opts.noReport {
		if err := writeReport(result, spikes, opts.report, cfg); err != nil {
			return err
		}
		if opts.open {
			if err := openReport(opts.report); err != nil {
				return fmt.Errorf("opening report: %w", err)
			}
		}
	} else {
		fmt.Println("Report skipped (--no-report)")
	}

	if opts.jsonPath != "" {
		data := barrage.NewReportData(result, spikes)
		data.Duration = time.Duration(cfg.Duration).String()
		data.Ramp = time.Duration(cfg.Ramp).String()
		data.Concurrency = cfg.Concurrency
		if err := barrage.ExportJSON(data, opts.jsonPath); err != nil {
			return fmt.Errorf("writing JSON: %w", err)
		}
		fmt.Printf("JSON written to %s\n", opts.jsonPath)
	}
	return nil
}

// warnDeprecatedRunFlags prints a one-line rename hint for each pre-0.6 flag
// the user still invoked. The aliases bind to the same options as the new
// names, so behavior is unchanged; this just nudges configs onto capacity:.
func warnDeprecatedRunFlags(cmd *cobra.Command) {
	deprecated := []struct{ flag, hint string }{
		{"auto-ramp", "--capacity"},
		{"ramp-max-concurrency", "--capacity-max-concurrency"},
		{"ramp-step-duration", "--capacity-step-duration"},
	}
	for _, d := range deprecated {
		if cmd.Flags().Changed(d.flag) {
			fmt.Fprintf(os.Stderr, "warning: --%s is deprecated; use %s\n", d.flag, d.hint)
		}
	}
}

func runCapacitySweep(opts *runOptions, cfg *barrage.OrchestratorConfig) error {
	stats := &barrage.RunStats{}
	cfg.Stats = stats
	cfg.Quiet = true

	sweepCfg := *cfg.Capacity
	res, err := barrage.RunCapacitySweep(*cfg, sweepCfg, opts.httpThreshold, opts.dbThreshold, opts.redisThreshold)
	if err != nil {
		return fmt.Errorf("capacity sweep failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[barrage] sweep done · %s\n", stats.Summary())
	printCapacityTable(res)
	if res.BreakAt != 0 {
		fmt.Printf("\nbroke at concurrency %d (last ok %d)\n", res.BreakAt, res.LastOK)
	} else {
		fmt.Printf("\nheld to concurrency %d — no break found\n", res.LastOK)
	}

	if !opts.noReport {
		data := barrage.ReportData{CapacitySearch: res}
		stepDur := time.Duration(cfg.Capacity.StepDuration)
		if stepDur <= 0 {
			stepDur = 10 * time.Second
		}
		total := stepDur * time.Duration(len(res.Steps))
		data.Duration = total.String()
		data.Ramp = time.Duration(cfg.Ramp).String()
		data.Concurrency = cfg.Capacity.MaxConcurrency
		file, err := os.Create(opts.report)
		if err != nil {
			return fmt.Errorf("creating report %q: %w", opts.report, err)
		}
		if err := barrage.RenderHTML(data, "templates/report.html", file); err != nil {
			file.Close()
			return fmt.Errorf("rendering report: %w", err)
		}
		file.Close()
		fmt.Printf("Report written to %s\n", opts.report)
		if opts.open {
			if err := openReport(opts.report); err != nil {
				return fmt.Errorf("opening report: %w", err)
			}
		}
	} else {
		fmt.Println("Report skipped (--no-report)")
	}

	if opts.jsonPath != "" {
		data := barrage.ReportData{CapacitySearch: res}
		stepDur := time.Duration(cfg.Capacity.StepDuration)
		if stepDur <= 0 {
			stepDur = 10 * time.Second
		}
		data.Duration = (stepDur * time.Duration(len(res.Steps))).String()
		data.Ramp = time.Duration(cfg.Ramp).String()
		data.Concurrency = cfg.Capacity.MaxConcurrency
		if err := barrage.ExportJSON(data, opts.jsonPath); err != nil {
			return fmt.Errorf("writing JSON: %w", err)
		}
		fmt.Printf("JSON written to %s\n", opts.jsonPath)
	}
	return nil
}

func printCapacityTable(res *barrage.CapacityResult) {
	if res == nil {
		return
	}
	t := cliui.NewTable(
		cliui.Column{Name: "CONCURRENCY", Align: cliui.Right},
		cliui.Column{Name: "REQUESTS", Align: cliui.Right},
		cliui.Column{Name: "P99", Align: cliui.Right},
		cliui.Column{Name: "SUCCESS", Align: cliui.Right},
		cliui.Column{Name: "VERDICT"},
		cliui.Column{Name: "BROKEN BY"},
	)
	for _, s := range res.Steps {
		verdict := "ok"
		cause := "-"
		if s.Broken {
			verdict = cliui.VerdictColorize("BROKEN")
			cause = strings.Join(s.BrokenBy, ",")
		}
		t.Row(
			strconv.Itoa(s.Concurrency),
			strconv.Itoa(int(s.Requests)),
			s.P99.String(),
			cliui.SuccessColorize(s.Success*100),
			verdict,
			cause,
		)
	}
	fmt.Println(t.Render())
}

func loadJSONReport(path string) (*barrage.JSONReport, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading report %q: %w", path, err)
	}
	var report barrage.JSONReport
	if err := json.Unmarshal(buf, &report); err != nil {
		return nil, fmt.Errorf("parsing report %q: %w", path, err)
	}
	return &report, nil
}

func runCompare(opts *compareOptions) error {
	if opts.baseline == "" || opts.current == "" {
		return errors.New("both --baseline and --current are required")
	}

	baseline, err := loadJSONReport(opts.baseline)
	if err != nil {
		return err
	}
	current, err := loadJSONReport(opts.current)
	if err != nil {
		return err
	}

	rows := barrage.CompareRun(baseline, current)
	fmt.Println()
	fmt.Printf("comparing %s -> %s (fail-on %s)\n", opts.baseline, opts.current, opts.failOn)

	t := cliui.NewTable(
		cliui.Column{Name: "RUNNER"},
		cliui.Column{Name: "BASELINE_P99", Align: cliui.Right},
		cliui.Column{Name: "CURRENT_P99", Align: cliui.Right},
		cliui.Column{Name: "CHANGE", Align: cliui.Right},
		cliui.Column{Name: "VERDICT"},
	)
	var failed bool
	for _, r := range rows {
		verdict := cliui.VerdictColorize("ok")
		if r.New {
			verdict = cliui.Dim("NEW")
		} else if r.Regressed(opts.failOn.Milliseconds()) {
			verdict = cliui.VerdictColorize("REGRESSION")
			failed = true
		}
		t.Row(
			r.Name,
			fmt.Sprintf("%dms", r.BaselineP99),
			fmt.Sprintf("%dms", r.CurrentP99),
			fmt.Sprintf("%+d%%", r.PctChange),
			verdict,
		)
	}
	fmt.Println(t.Render())

	if opts.report != "" {
		file, err := os.Create(opts.report)
		if err != nil {
			return fmt.Errorf("creating report %q: %w", opts.report, err)
		}
		data := barrage.NewCompareReportData(baseline, current, opts.failOn.Milliseconds())
		data.BaselineName = opts.baseline
		data.CurrentName = opts.current
		if err := barrage.RenderCompare(data, "templates/compare.html", file); err != nil {
			file.Close()
			return fmt.Errorf("rendering compare report: %w", err)
		}
		file.Close()
		fmt.Printf("Comparison report written to %s\n", opts.report)
		if opts.open {
			if err := openReport(opts.report); err != nil {
				return fmt.Errorf("opening report: %w", err)
			}
		}
	}

	if failed {
		return errors.New("regression detected against --fail-on budget")
	}
	return nil
}

// newRunnerTable is the per-runner summary used by `run`. Latency columns are
// right-aligned so magnitudes line up; the runner name and status codes read
// better left-aligned.
func newRunnerTable() *cliui.Table {
	return cliui.NewTable(
		cliui.Column{Name: "RUNNER"},
		cliui.Column{Name: "REQUESTS", Align: cliui.Right},
		cliui.Column{Name: "SUCCESS", Align: cliui.Right},
		cliui.Column{Name: "RATE", Align: cliui.Right},
		cliui.Column{Name: "MEAN", Align: cliui.Right},
		cliui.Column{Name: "P50", Align: cliui.Right},
		cliui.Column{Name: "P95", Align: cliui.Right},
		cliui.Column{Name: "P99", Align: cliui.Right},
		cliui.Column{Name: "MAX", Align: cliui.Right},
	)
}

func printResults(result *barrage.OrchestratorResult, verbose bool) {
	if result == nil {
		return
	}
	summary := barrage.NewReportData(result, barrage.CorrelationResult{})
	t := newRunnerTable()
	for _, r := range summary.Runners {
		t.Row(
			strings.ToLower(r.Name),
			strconv.Itoa(int(r.Requests)),
			cliui.SuccessColorize(r.Success),
			fmt.Sprintf("%.1f/s", r.Rate),
			r.Mean.String(), r.P50.String(), r.P95.String(), r.P99.String(), r.Max.String(),
		)
	}
	if out := t.Render(); out != "" {
		fmt.Println(out)
	}

	if !verbose {
		return
	}
	if result.HTTPResult != nil {
		printHTTPBuckets(result.HTTPResult.Buckets)
	}
	if result.DBResult != nil {
		printBucketTable("db", result.DBResult.Buckets)
	}
	if result.RedisResult != nil {
		printBucketTable("redis", result.RedisResult.Buckets)
	}
	if len(result.ScenarioAggregates) > 0 {
		for _, agg := range result.ScenarioAggregates {
			if agg.Stats == nil {
				continue
			}
			name := agg.Name
			if name == "" {
				name = "scenario"
			}
			printBucketTable(name, agg.Stats.Buckets)
		}
	} else if result.ScenarioStats != nil {
		name := result.ScenarioName
		if name == "" {
			name = "scenario"
		}
		printBucketTable(name, result.ScenarioStats.Buckets)
	}
}

// printHTTPBuckets renders the per-bucket HTTP table. Separate from
// printBucketTable because HTTP buckets key on a time.Time and carry their own
// status-code map, while storage buckets key on a unix second.
func printHTTPBuckets(buckets []barrage.HTTPBucket) {
	if len(buckets) == 0 {
		return
	}
	t := cliui.NewTable(
		cliui.Column{Name: "TIME"},
		cliui.Column{Name: "REQUESTS", Align: cliui.Right},
		cliui.Column{Name: "P50", Align: cliui.Right},
		cliui.Column{Name: "P99", Align: cliui.Right},
		cliui.Column{Name: "STATUS"},
	)
	for _, b := range buckets {
		t.Row(
			b.Start.Format("15:04:05"),
			strconv.Itoa(int(b.Requests)),
			b.P50.String(), b.P99.String(),
			formatStatusCodes(b.StatusCodes),
		)
	}
	fmt.Printf("\n%s buckets\n", cliui.Accent("http"))
	fmt.Println(t.Render())
}

// printBucketTable renders one storage runner's per-bucket stats as a table.
func printBucketTable(runner string, buckets []barrage.Bucket) {
	if len(buckets) == 0 {
		return
	}
	t := cliui.NewTable(
		cliui.Column{Name: "TIME"},
		cliui.Column{Name: "REQUESTS", Align: cliui.Right},
		cliui.Column{Name: "P50", Align: cliui.Right},
		cliui.Column{Name: "P99", Align: cliui.Right},
	)
	for _, b := range buckets {
		t.Row(
			time.Unix(b.Start, 0).Format("15:04:05"),
			strconv.Itoa(int(b.Requests)),
			b.P50.String(), b.P99.String(),
		)
	}
	fmt.Printf("\n%s buckets\n", cliui.Accent(runner))
	fmt.Println(t.Render())
}

// printSpikes renders correlated spikes as an aligned table. Masked spikes
// (storage crossed its threshold while HTTP stayed below the HTTP threshold)
// are flagged as such.
func printSpikes(spikes barrage.CorrelationResult, httpThreshold time.Duration) {
	fmt.Println()
	if len(spikes.Spikes) == 0 {
		fmt.Println("correlated spikes: none")
		return
	}
	t := cliui.NewTable(
		cliui.Column{Name: "TIME"},
		cliui.Column{Name: "RUNNER"},
		cliui.Column{Name: "HTTP_P99", Align: cliui.Right},
		cliui.Column{Name: "STORAGE_P99", Align: cliui.Right},
		cliui.Column{Name: "NOTE"},
	)
	for _, s := range spikes.Spikes {
		httpP99 := s.HTTPLatency.String()
		note := ""
		if s.Masked {
			httpP99 = fmt.Sprintf("<%s", httpThreshold)
			note = s.Runner + "-only"
		}
		t.Row(
			time.Unix(s.BucketIndex, 0).Format("15:04:05"), s.Runner,
			httpP99, s.StorageLatency.String(), cliui.SpikeNoteColorize(note),
		)
	}
	fmt.Println(t.Render())
}

func effectiveConcurrency(cfg *barrage.OrchestratorConfig) int {
	if cfg.Concurrency > 0 {
		return cfg.Concurrency
	}
	return barrage.DefaultConcurrency
}

func configuredRates(cfg *barrage.OrchestratorConfig) []string {
	rates := make([]string, 0, 4)
	if cfg.HTTP != nil {
		rates = append(rates, fmt.Sprintf("http %d/s", cfg.HTTP.Rate))
	}
	if cfg.DB != nil {
		rates = append(rates, fmt.Sprintf("db %d/s", cfg.DB.Rate))
	}
	if cfg.Redis != nil {
		rates = append(rates, fmt.Sprintf("redis %d/s", cfg.Redis.Rate))
	}
	for _, sc := range cfg.Scenario {
		name := sc.Name
		if name == "" {
			name = "scenario"
		}
		rates = append(rates, fmt.Sprintf("%s %d steps w=%d", name, len(sc.Steps), sc.Weight))
	}
	return rates
}

func writeReport(result *barrage.OrchestratorResult, spikes barrage.CorrelationResult, path string, cfg *barrage.OrchestratorConfig) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating report %q: %w", path, err)
	}
	defer file.Close()
	data := barrage.NewReportData(result, spikes)
	data.Duration = time.Duration(cfg.Duration).String()
	data.Ramp = time.Duration(cfg.Ramp).String()
	data.Concurrency = cfg.Concurrency
	if err := barrage.RenderHTML(data, "templates/report.html", file); err != nil {
		return fmt.Errorf("rendering report: %w", err)
	}
	fmt.Printf("Report written to %s\n", path)
	return nil
}

func openReport(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

func formatStatusCodes(codes map[string]int) string {
	keys := make([]string, 0, len(codes))
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, codes[k]))
	}
	return strings.Join(parts, ", ")
}
