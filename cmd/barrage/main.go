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
	"text/tabwriter"
	"time"

	"github.com/codetesla51/barrage"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

var version = "v0.3.1"

const banner = `     ________  ________  ________  ________  ________  ________  _______
    |\   __  \|\   __  \|\   __  \|\   __  \|\   __  \|\   ____\|\  ___ \
    \ \  \|\ /\ \  \|\  \ \  \|\  \ \  \|\  \ \  \|\  \ \  \___|\ \   __/|
     \ \   __  \ \   __  \ \   _  _\ \   _  _\ \   __  \ \  \  __\ \  \_|/__
      \ \  \|\  \ \  \ \  \ \  \\  \\ \  \\  \\ \  \ \  \ \  \|\  \ \  \_|\ \
       \ \_______\ \__\ \__\ \__\\ _\\ \__\\ _\\ \__\ \__\ \_______\ \_______\
        \|_______|\|__|\|__|\|__|\|__|\|__|\|__|\|__|\|__|\|_______|\|_______|`

type runOptions struct {
	config         string
	report         string
	noReport       bool
	open           bool
	duration       time.Duration
	bucketWidth    time.Duration
	ramp           time.Duration
	concurrency    int
	jsonPath       string
	httpThreshold  time.Duration
	dbThreshold    time.Duration
	redisThreshold time.Duration
	verbose        bool
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
			fmt.Printf("barrage %s\n", version)
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

	fmt.Println(banner)
	fmt.Println()
	fmt.Printf("barrage %s\n", version)
	fmt.Printf("duration %s · bucket %s · concurrency %d · ramp %s\n",
		time.Duration(cfg.Duration), time.Duration(cfg.BucketWidth), effectiveConcurrency(cfg), time.Duration(cfg.Ramp))
	if rates := configuredRates(cfg); len(rates) > 0 {
		fmt.Printf("rates    %s\n", strings.Join(rates, " · "))
	}

	fmt.Println()

	result, err := barrage.Orchestrator(*cfg)
	if err != nil {
		return fmt.Errorf("load test failed: %w", err)
	}

	printResults(result, opts.verbose)

	var spikes barrage.CorrelationResult
	if result.HTTPResult != nil && (result.DBResult != nil || result.RedisResult != nil) {
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

	table := make([][]string, 0, len(rows))
	var failed bool
	for _, r := range rows {
		verdict := "ok"
		if r.Regressed(opts.failOn.Milliseconds()) {
			verdict = "REGRESSION"
			failed = true
		}
		table = append(table, []string{
			r.Name,
			fmt.Sprintf("%dms", r.BaselineP99),
			fmt.Sprintf("%dms", r.CurrentP99),
			fmt.Sprintf("%+d%%", r.PctChange),
			verdict,
		})
	}
	writeTable([]string{"RUNNER", "BASELINE_P99", "CURRENT_P99", "CHANGE", "VERDICT"}, table)

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

func printResults(result *barrage.OrchestratorResult, verbose bool) {
	if result == nil {
		return
	}
	summary := barrage.NewReportData(result, barrage.CorrelationResult{})
	rows := make([][]string, 0, len(summary.Runners))
	for _, r := range summary.Runners {
		rows = append(rows, []string{
			strings.ToLower(r.Name),
			strconv.Itoa(int(r.Requests)),
			fmt.Sprintf("%.1f%%", r.Success),
			fmt.Sprintf("%.1f/s", r.Rate),
			r.Mean.String(), r.P50.String(), r.P95.String(), r.P99.String(), r.Max.String(),
			formatStatusCodes(r.StatusCodes),
		})
	}
	writeTable([]string{"RUNNER", "REQUESTS", "SUCCESS", "RATE", "MEAN", "P50", "P95", "P99", "MAX", "STATUS"}, rows)

	if !verbose {
		return
	}
	if result.HTTPResult != nil {
		rows := make([][]string, 0, len(result.HTTPResult.Buckets))
		for _, b := range result.HTTPResult.Buckets {
			rows = append(rows, []string{b.Start.Format("15:04:05"), strconv.Itoa(int(b.Requests)), b.P50.String(), b.P99.String(), formatStatusCodes(b.StatusCodes)})
		}
		fmt.Printf("\nhttp buckets\n")
		writeTable([]string{"TIME", "REQUESTS", "P50", "P99", "STATUS"}, rows)
	}
	if result.DBResult != nil {
		printBucketTable("db", result.DBResult.Buckets)
	}
	if result.RedisResult != nil {
		printBucketTable("redis", result.RedisResult.Buckets)
	}
}

// writeTable prints a header and rows as an aligned column table.
func writeTable(header []string, rows [][]string) {
	table := tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintln(table, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(table, strings.Join(r, "\t"))
	}
	table.Flush()
}

// printBucketTable renders one storage runner's per-bucket stats as a table.
func printBucketTable(runner string, buckets []barrage.Bucket) {
	rows := make([][]string, 0, len(buckets))
	for _, b := range buckets {
		rows = append(rows, []string{time.Unix(b.Start, 0).Format("15:04:05"), strconv.Itoa(int(b.Requests)), b.P50.String(), b.P99.String(), ""})
	}
	fmt.Printf("\n%s buckets\n", runner)
	writeTable([]string{"TIME", "REQUESTS", "P50", "P99", "STATUS"}, rows)
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
	rows := make([][]string, 0, len(spikes.Spikes))
	for _, s := range spikes.Spikes {
		httpP99 := s.HTTPLatency.String()
		note := ""
		if s.Masked {
			httpP99 = fmt.Sprintf("<%s", httpThreshold)
			note = s.Runner + "-only"
		}
		rows = append(rows, []string{
			time.Unix(s.BucketIndex, 0).Format("15:04:05"), s.Runner, httpP99, s.StorageLatency.String(), note,
		})
	}
	fmt.Println("correlated spikes")
	writeTable([]string{"TIME", "RUNNER", "HTTP_P99", "STORAGE_P99", "NOTE"}, rows)
}

func effectiveConcurrency(cfg *barrage.OrchestratorConfig) int {
	if cfg.Concurrency > 0 {
		return cfg.Concurrency
	}
	return barrage.DefaultConcurrency
}

func configuredRates(cfg *barrage.OrchestratorConfig) []string {
	rates := make([]string, 0, 3)
	if cfg.HTTP != nil {
		rates = append(rates, fmt.Sprintf("http %d/s", cfg.HTTP.Rate))
	}
	if cfg.DB != nil {
		rates = append(rates, fmt.Sprintf("db %d/s", cfg.DB.Rate))
	}
	if cfg.Redis != nil {
		rates = append(rates, fmt.Sprintf("redis %d/s", cfg.Redis.Rate))
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
