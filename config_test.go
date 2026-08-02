package barrage

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigFull(t *testing.T) {
	path := writeConfig(t, `
duration: 10s
bucket_width: 1s
http:
  rate: 10
  target:
    method: POST
    url: http://example.com/orders
    body: '{"foo":"bar"}'
    header:
      content-type: [application/json]
      x-foo: bar
db:
  rate: 5
  target:
    driver: postgres
    conn: postgres://localhost/db
    query_type: read
    queries:
      - query: SELECT 1
        weight: 70
      - query: SELECT 2
        weight: 20
    args: ["a", 1]
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Duration != Duration(10*time.Second) {
		t.Errorf("duration = %v, want 10s", cfg.Duration)
	}
	if cfg.BucketWidth != Duration(time.Second) {
		t.Errorf("bucket_width = %v, want 1s", cfg.BucketWidth)
	}

	if cfg.HTTP == nil {
		t.Fatal("expected http section to be present")
	}
	if cfg.HTTP.Rate != 10 {
		t.Errorf("http rate = %d, want 10", cfg.HTTP.Rate)
	}
	target := cfg.HTTP.Target
	if target.Method != "POST" {
		t.Errorf("method = %q, want POST", target.Method)
	}
	if target.URL != "http://example.com/orders" {
		t.Errorf("url = %q", target.URL)
	}
	if string(target.Body) != `{"foo":"bar"}` {
		t.Errorf("body = %q, want %q", target.Body, `{"foo":"bar"}`)
	}
	if got := target.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := target.Header.Get("X-Foo"); got != "bar" {
		t.Errorf("X-Foo = %q, want bar (scalar header value)", got)
	}

	if cfg.DB == nil {
		t.Fatal("expected db section to be present")
	}
	if cfg.DB.Rate != 5 {
		t.Errorf("db rate = %d, want 5", cfg.DB.Rate)
	}
	dbTarget := cfg.DB.Target
	if dbTarget.Driver != "postgres" {
		t.Errorf("driver = %q", dbTarget.Driver)
	}
	if dbTarget.Conn != "postgres://localhost/db" {
		t.Errorf("conn = %q", dbTarget.Conn)
	}
	if dbTarget.QueryType != "read" {
		t.Errorf("query_type = %q", dbTarget.QueryType)
	}
	if len(dbTarget.Query) != 2 || dbTarget.Query[0].Query != "SELECT 1" || dbTarget.Query[0].Weight != 70 {
		t.Errorf("queries = %+v", dbTarget.Query)
	}
	if len(dbTarget.Args) != 2 || dbTarget.Args[0] != "a" || dbTarget.Args[1] != 1 {
		t.Errorf("args = %#v", dbTarget.Args)
	}
}

func TestLoadConfigHTTPOnly(t *testing.T) {
	path := writeConfig(t, `
duration: 2s
http:
  rate: 5
  target:
    url: http://example.com
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.HTTP == nil {
		t.Error("expected http section to be present")
	}
	if cfg.DB != nil {
		t.Error("expected db section to be absent")
	}
}

func TestLoadConfigDBOnly(t *testing.T) {
	path := writeConfig(t, `
duration: 2s
db:
  rate: 5
  target:
    driver: postgres
    conn: postgres://localhost/db
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.HTTP != nil {
		t.Error("expected http section to be absent")
	}
	if cfg.DB == nil {
		t.Error("expected db section to be present")
	}
}

func TestLoadConfigNoRunners(t *testing.T) {
	path := writeConfig(t, "duration: 2s\n")
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error when no runner is configured")
	}
}

func TestLoadConfigUnknownKey(t *testing.T) {
	path := writeConfig(t, `
duration: 2s
http:
  rate: 5
  target:
    url: http://example.com
httpx: {}
`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for unknown top-level key")
	}

	path = writeConfig(t, `
duration: 2s
http:
  rate: 5
  targit:
    url: http://example.com
`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for unknown key inside http section")
	}

	path = writeConfig(t, `
duration: 2s
http:
  rate: 5
  target:
    url: http://example.com
    badkey: nope
`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for unknown key inside target")
	}
}

func TestLoadConfigBadDuration(t *testing.T) {
	path := writeConfig(t, "duration: not-a-duration\n")
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestLoadConfigZeroRate(t *testing.T) {
	path := writeConfig(t, `
duration: 2s
http:
  rate: 0
  target:
    url: http://example.com
`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for zero http rate")
	}
}

func TestLoadConfigEmptyFile(t *testing.T) {
	path := writeConfig(t, "")
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for empty config file")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected error for missing config file")
	}
}

func TestOrchestratorHTTPOnly(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := OrchestratorConfig{
		Duration:    Duration(time.Second),
		BucketWidth: Duration(time.Second),
		HTTP: &HTTPRunnerConfig{
			Target: HTTPTarget{URL: ts.URL},
			Rate:   10,
		},
	}
	res, err := Orchestrator(cfg)
	if err != nil {
		t.Fatalf("Orchestrator returned error: %v", err)
	}
	if res.HTTPResult == nil {
		t.Error("expected HTTPResult to be populated")
	} else if res.HTTPResult.Requests == 0 {
		t.Error("expected HTTP requests to be fired")
	}
	if res.DBResult != nil {
		t.Error("expected DBResult to be nil when no db section is configured")
	}
}

func TestLoadConfigExampleFile(t *testing.T) {
	cfg, err := LoadConfig("config.yaml")
	if err != nil {
		t.Fatalf("example config failed to load: %v", err)
	}
	if cfg.HTTP == nil || cfg.DB == nil {
		t.Fatalf("example config should configure both runners, got http=%v db=%v", cfg.HTTP, cfg.DB)
	}
	if len(cfg.DB.Target.Query) != 3 {
		t.Errorf("expected 3 queries in example, got %d", len(cfg.DB.Target.Query))
	}
	if !strings.HasPrefix(cfg.HTTP.Target.URL, "http://") {
		t.Errorf("example http url = %q", cfg.HTTP.Target.URL)
	}
}
