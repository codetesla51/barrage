package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/codetesla51/barrage"
	_ "github.com/lib/pq"
)

func formatStatusCodes(codes map[string]int) string {
	keys := make([]string, 0, len(codes))
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, codes[k]))
	}
	return fmt.Sprintf("%v", parts)
}

func main() {
	cfg, err := barrage.LoadConfig("config.yaml")
	if err != nil {
		panic(err)
	}

	result, err := barrage.Orchestrator(*cfg)
	if err != nil {
		panic(err)
	}

	if result.HTTPResult != nil {
		fmt.Println("=== HTTP ===")
		fmt.Printf("Requests: %d\n", result.HTTPResult.Requests)
		fmt.Printf("Success rate: %.2f%%\n", result.HTTPResult.Success*100)
		fmt.Printf("p50: %s\n", result.HTTPResult.P50)
		fmt.Printf("p99: %s\n", result.HTTPResult.P99)
		fmt.Printf("Status codes: %s\n", formatStatusCodes(result.HTTPResult.StatusCodes))
		fmt.Printf("Buckets: %d\n", len(result.HTTPResult.Buckets))
		for _, b := range result.HTTPResult.Buckets {
			fmt.Printf("  [%s] requests=%d p50=%s p99=%s status=%s\n",
				b.Start.Format("15:04:05"), b.Requests, b.P50, b.P99, formatStatusCodes(b.StatusCodes))
		}
	}

	if result.DBResult != nil {
		fmt.Println("\n=== DB ===")
		fmt.Printf("Requests: %d\n", result.DBResult.Requests)
		fmt.Printf("Success rate: %.2f%%\n", result.DBResult.Success*100)
		fmt.Printf("p50: %s\n", result.DBResult.P50)
		fmt.Printf("p99: %s\n", result.DBResult.P99)
		fmt.Printf("Buckets: %d\n", len(result.DBResult.Buckets))
		for _, b := range result.DBResult.Buckets {
			fmt.Printf("  [%s] requests=%d p50=%s p99=%s\n",
				time.Unix(b.Start, 0).Format("15:04:05"), b.Requests, b.P50, b.P99)
		}
	}

	if result.RedisResult != nil {
		fmt.Println("\n=== Redis ===")
		fmt.Printf("Requests: %d\n", result.RedisResult.Requests)
		fmt.Printf("Success rate: %.2f%%\n", result.RedisResult.Success*100)
		fmt.Printf("p50: %s\n", result.RedisResult.P50)
		fmt.Printf("p99: %s\n", result.RedisResult.P99)
		fmt.Printf("Buckets: %d\n", len(result.RedisResult.Buckets))
		for _, b := range result.RedisResult.Buckets {
			fmt.Printf("  [%s] requests=%d p50=%s p99=%s\n",
				time.Unix(b.Start, 0).Format("15:04:05"), b.Requests, b.P50, b.P99)
		}
	}

	if result.HTTPResult != nil && result.DBResult != nil {
		const (
			httpSpikeThreshold = 100 * time.Millisecond
			dbSpikeThreshold   = 100 * time.Millisecond
		)
		fmt.Println("\n=== Correlated Spikes ===")
		spikes := barrage.Correlate(result, httpSpikeThreshold, dbSpikeThreshold)
		fmt.Printf("Spikes: %d\n", len(spikes.Spikes))
		for _, s := range spikes.Spikes {
			fmt.Printf("  bucket=%d http_p99=%s db_p99=%s\n",
				s.BucketIndex, s.HTTPLatency, s.DBLatency)
		}

		reportFile, err := os.Create("report.html")
		if err != nil {
			panic(err)
		}
		defer reportFile.Close()
		reportData := barrage.NewReportData(result, spikes)
		if err := barrage.RenderHTML(reportData, "templates/report.html", reportFile); err != nil {
			panic(err)
		}
		fmt.Println("Report written to report.html")
	}
}
