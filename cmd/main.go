package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/codetesla51/barrage"
	_ "github.com/lib/pq"
	"github.com/spf13/cobra"
)

var version = "dev"

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
	root.AddCommand(newRunCmd(), newVersionCmd())
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
	fmt.Printf("running for %s (bucket width %s)\n", time.Duration(cfg.Duration), time.Duration(cfg.BucketWidth))
	if cfg.Ramp > 0 {
		fmt.Printf("ramping rate from 0 to full over %s\n", time.Duration(cfg.Ramp))
	}
	if cfg.Concurrency > 0 {
		fmt.Printf("concurrency %d\n", cfg.Concurrency)
	} else {
		fmt.Printf("concurrency default %d (db/redis); http auto-scales\n", barrage.DefaultConcurrency)
	}

	result, err := barrage.Orchestrator(*cfg)
	if err != nil {
		return fmt.Errorf("load test failed: %w", err)
	}

	printResults(result, opts.verbose)

	var spikes barrage.CorrelationResult
	if result.HTTPResult != nil && (result.DBResult != nil || result.RedisResult != nil) {
		fmt.Println("\n=== Correlated Spikes ===")
		spikes = barrage.Correlate(result, opts.httpThreshold, opts.dbThreshold, opts.redisThreshold)
		if len(spikes.Spikes) == 0 {
			fmt.Println("  none")
		}
		for _, s := range spikes.Spikes {
			if s.Masked {
				fmt.Printf("  %s  %s_p99=%s  (%s-only: http stayed under %s)\n",
					time.Unix(s.BucketIndex, 0).Format("15:04:05"), s.Runner, s.StorageLatency, s.Runner, opts.httpThreshold)
			} else {
				fmt.Printf("  %s  http_p99=%s  %s_p99=%s\n",
					time.Unix(s.BucketIndex, 0).Format("15:04:05"), s.HTTPLatency, s.Runner, s.StorageLatency)
			}
		}
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

func printResults(result *barrage.OrchestratorResult, verbose bool) {
	if result == nil {
		return
	}
	summary := barrage.NewReportData(result, barrage.CorrelationResult{})
	for _, r := range summary.Runners {
		fmt.Printf("\n=== %s ===\n", r.Name)
		fmt.Printf("  requests     %d\n", r.Requests)
		fmt.Printf("  success      %.1f%%\n", r.Success)
		fmt.Printf("  p50          %s\n", r.P50)
		fmt.Printf("  p95          %s\n", r.P95)
		fmt.Printf("  p99          %s\n", r.P99)
		fmt.Printf("  max          %s\n", r.Max)
		fmt.Printf("  mean         %s\n", r.Mean)
		fmt.Printf("  rate         %.1f/s\n", r.Rate)
		fmt.Printf("  throughput   %.1f/s\n", r.Throughput)
		if len(r.StatusCodes) > 0 {
			fmt.Printf("  status       %s\n", formatStatusCodes(r.StatusCodes))
		}
	}

	if !verbose {
		return
	}
	if result.HTTPResult != nil {
		fmt.Println("\n=== HTTP buckets ===")
		for _, b := range result.HTTPResult.Buckets {
			fmt.Printf("  [%s] requests=%d p50=%s p99=%s status=%s\n",
				b.Start.Format("15:04:05"), b.Requests, b.P50, b.P99, formatStatusCodes(b.StatusCodes))
		}
	}
	if result.DBResult != nil {
		fmt.Println("\n=== DB buckets ===")
		for _, b := range result.DBResult.Buckets {
			fmt.Printf("  [%s] requests=%d p50=%s p99=%s\n",
				time.Unix(b.Start, 0).Format("15:04:05"), b.Requests, b.P50, b.P99)
		}
	}
	if result.RedisResult != nil {
		fmt.Println("\n=== Redis buckets ===")
		for _, b := range result.RedisResult.Buckets {
			fmt.Printf("  [%s] requests=%d p50=%s p99=%s\n",
				time.Unix(b.Start, 0).Format("15:04:05"), b.Requests, b.P50, b.P99)
		}
	}
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
