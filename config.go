package barrage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
//
// The http, db, and redis sections are optional: a missing section means that
// runner is skipped, so a file may configure any subset of them. At least one
// runner must be present. Unknown YAML keys are rejected so that a misspelled
// key surfaces as an error instead of being silently ignored.
func LoadConfig(path string) (*OrchestratorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &OrchestratorConfig{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config file %q is empty", path)
		}
		return nil, err
	}
	if cfg.HTTP == nil && cfg.DB == nil && cfg.Redis == nil {
		return nil, errors.New("config must specify at least one runner: http, db, or redis")
	}
	if cfg.HTTP != nil && cfg.HTTP.Rate <= 0 {
		return nil, errors.New("http rate must be greater than zero")
	}
	if cfg.DB != nil && cfg.DB.Rate <= 0 {
		return nil, errors.New("db rate must be greater than zero")
	}
	if cfg.Redis != nil && cfg.Redis.Rate <= 0 {
		return nil, errors.New("redis rate must be greater than zero")
	}
	return cfg, nil
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
