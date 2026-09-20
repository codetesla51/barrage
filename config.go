package barrage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that can be loaded from a YAML duration string
// (e.g. "10s", "1m30s") or from an integer number of nanoseconds.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string or integer: %w", err)
	}
	if parsed, err := time.ParseDuration(s); err == nil {
		*d = Duration(parsed)
		return nil
	}
	var n int64
	if err := node.Decode(&n); err == nil {
		*d = Duration(n)
		return nil
	}
	return fmt.Errorf("invalid duration %q", s)
}

// LoadConfig reads a YAML file and populates an OrchestratorConfig from it.
func LoadConfig(path string) (*OrchestratorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadConfigBytes(data, path)
}

// LoadConfigBytes parses YAML bytes with the exact same rules as LoadConfig.
// It backs the web UI's import and validate endpoints so the browser form and
// the CLI loader can never disagree about what is valid.
func LoadConfigBytes(data []byte) (*OrchestratorConfig, error) {
	return loadConfigBytes(data, "")
}

func loadConfigBytes(data []byte, path string) (*OrchestratorConfig, error) {
	cfg := &OrchestratorConfig{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			if path != "" {
				return nil, fmt.Errorf("config file %q is empty", path)
			}
			return nil, errors.New("config is empty")
		}
		return nil, err
	}
	if len(cfg.DeprecatedScenarios) > 0 {
		return nil, errors.New("scenarios: is renamed to scenario: (singular)")
	}
	// auto_ramp: is the pre-0.6 name for the capacity sweep. Merge it so old
	// configs load unchanged, and flag it so the caller can print a warning.
	if cfg.Capacity == nil && cfg.DeprecatedAutoRamp != nil {
		cfg.Capacity = cfg.DeprecatedAutoRamp
		cfg.UsedDeprecatedAutoRamp = true
	}
	hasScenario := len(cfg.Scenario) > 0
	if cfg.HTTP == nil && cfg.DB == nil && cfg.Redis == nil && !hasScenario {
		return nil, errors.New("config must specify at least one runner: http, db, redis, or scenario")
	}
	if hasScenario && cfg.HTTP != nil {
		return nil, errors.New("scenario mode cannot be combined with http section")
	}
	for idx, sc := range cfg.Scenario {
		if sc.Weight == 0 {
			cfg.Scenario[idx].Weight = 1
		} else if sc.Weight < 0 {
			return nil, fmt.Errorf("scenario[%d] %q: weight must not be negative", idx, sc.Name)
		}
		if len(sc.Steps) == 0 {
			return nil, fmt.Errorf("scenario[%d] %q must have at least one step", idx, sc.Name)
		}
		for i, step := range sc.Steps {
			if !isValidMethod(step.Method) {
				return nil, fmt.Errorf("scenario[%d] step %d: invalid method %q", idx, i, step.Method)
			}
			if strings.TrimSpace(step.URL) == "" {
				return nil, fmt.Errorf("scenario[%d] step %d: url must not be empty", idx, i)
			}
		}
	}
	if cfg.HTTP != nil {
		if cfg.HTTP.Rate <= 0 {
			return nil, errors.New("http rate must be greater than zero")
		}
		if !isValidMethod(cfg.HTTP.Target.Method) && strings.TrimSpace(cfg.HTTP.Target.Method) != "" {
			return nil, fmt.Errorf("http target: invalid method %q", cfg.HTTP.Target.Method)
		}
		if strings.TrimSpace(cfg.HTTP.Target.URL) == "" {
			return nil, errors.New("http target url must not be empty")
		}
	}
	if cfg.DB != nil {
		if cfg.DB.Rate <= 0 {
			return nil, errors.New("db rate must be greater than zero")
		}
		if strings.TrimSpace(cfg.DB.Target.Conn) == "" {
			return nil, errors.New("db target conn must not be empty")
		}
		if strings.TrimSpace(cfg.DB.Target.Driver) == "" {
			return nil, errors.New("db target driver must not be empty")
		}
		if err := checkQueries(cfg.DB.Target.Query, "db"); err != nil {
			return nil, err
		}
		if cfg.DB.Target.MaxOpenConns < -1 {
			return nil, errors.New("db target max_open_conns must be -1 (unlimited) or greater")
		}
		if cfg.DB.Target.MaxIdleConns < 0 {
			return nil, errors.New("db target max_idle_conns must not be negative")
		}
		if cfg.DB.Target.ConnMaxLifetime < 0 {
			return nil, errors.New("db target conn_max_lifetime must not be negative")
		}
		if cfg.DB.Target.ConnMaxIdleTime < 0 {
			return nil, errors.New("db target conn_max_idle_time must not be negative")
		}
	}
	if cfg.Redis != nil {
		if cfg.Redis.Rate <= 0 {
			return nil, errors.New("redis rate must be greater than zero")
		}
		if strings.TrimSpace(cfg.Redis.Target.Addr) == "" {
			return nil, errors.New("redis target addr must not be empty")
		}
		if err := checkQueries(cfg.Redis.Target.Query, "redis"); err != nil {
			return nil, err
		}
	}
	if cfg.Capacity != nil {
		start := cfg.Concurrency
		if start <= 0 {
			start = DefaultConcurrency
		}
		if cfg.Capacity.MaxConcurrency <= 0 {
			return nil, errors.New("capacity max_concurrency must be greater than zero")
		}
		if cfg.Capacity.MaxConcurrency < start {
			return nil, fmt.Errorf("capacity max_concurrency %d below concurrency %d", cfg.Capacity.MaxConcurrency, start)
		}
		if cfg.Capacity.StepDuration < 0 {
			return nil, errors.New("capacity step_duration must not be negative")
		}
		if cfg.Capacity.StepDuration > 0 && time.Duration(cfg.Capacity.StepDuration) < time.Duration(cfg.BucketWidth) {
			return nil, errors.New("capacity step_duration must cover at least one bucket")
		}
	}
	if time.Duration(cfg.Duration) <= 0 {
		return nil, errors.New("duration must be greater than zero")
	}
	if cfg.Ramp < 0 {
		return nil, errors.New("ramp must not be negative")
	}
	if cfg.Concurrency < 0 {
		return nil, errors.New("concurrency must not be negative")
	}
	return cfg, nil
}

// checkQueries rejects empty query lists, empty query text, and weights that
// would break weighted picking (negative, or all-zero which panics rand.Intn).
func checkQueries(queries []QueryWeight, runner string) error {
	if len(queries) == 0 {
		return fmt.Errorf("%s target must list at least one query", runner)
	}
	total := 0
	for i, q := range queries {
		if strings.TrimSpace(q.Query) == "" {
			return fmt.Errorf("%s queries[%d]: query must not be empty", runner, i)
		}
		if q.Weight < 0 {
			return fmt.Errorf("%s queries[%d]: weight must not be negative", runner, i)
		}
		total += q.Weight
	}
	if total <= 0 {
		return fmt.Errorf("%s queries: total weight must be greater than zero", runner)
	}
	return nil
}

// checkKnownKeys rejects mapping keys that are not in the given set, mimicking
// yaml.v3's KnownFields mode for types that define their own UnmarshalYAML.
func checkKnownKeys(node *yaml.Node, known ...string) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	allowed := make(map[string]bool, len(known))
	for _, k := range known {
		allowed[k] = true
	}
	for i := 0; i < len(node.Content); i += 2 {
		if key := node.Content[i].Value; !allowed[key] {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	return nil
}

// UnmarshalYAML implements yaml.Unmarshaler for HTTPTarget so the request body
// can be written as a plain string and header values can be written as either a
// single string or a list of strings.
func (t *HTTPTarget) UnmarshalYAML(node *yaml.Node) error {
	type rawTarget struct {
		Method string         `yaml:"method"`
		URL    string         `yaml:"url"`
		Body   string         `yaml:"body"`
		Header map[string]any `yaml:"header"`
	}
	if err := checkKnownKeys(node, "method", "url", "body", "header"); err != nil {
		return err
	}
	var raw rawTarget
	if err := node.Decode(&raw); err != nil {
		return err
	}
	t.Method = raw.Method
	t.URL = raw.URL
	t.Body = []byte(raw.Body)
	t.Header = make(http.Header)
	for k, v := range raw.Header {
		key := http.CanonicalHeaderKey(k)
		switch value := v.(type) {
		case string:
			t.Header[key] = []string{value}
		case []any:
			parts := make([]string, 0, len(value))
			for _, p := range value {
				s, ok := p.(string)
				if !ok {
					return fmt.Errorf("header %q values must be strings", k)
				}
				parts = append(parts, s)
			}
			t.Header[key] = parts
		default:
			return fmt.Errorf("header %q must be a string or list of strings", k)
		}
	}
	return nil
}

func isValidMethod(m string) bool {
	if m == "" {
		return false
	}
	switch strings.ToUpper(m) {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE", "CONNECT":
		return true
	default:
		return false
	}
}
