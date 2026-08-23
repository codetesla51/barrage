package barrage

// Extract maps a variable name to a JSON path.
// Example: "token": "$.token" extracts the token field from a JSON response.
type Extract map[string]string

// Step is one HTTP request inside a scenario.
type Step struct {
	Method  string            `yaml:"method"`
	URL     string            `yaml:"url"`
	Body    string            `yaml:"body,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Extract Extract           `yaml:"extract,omitempty"`
}

// Scenario is a sequence of HTTP steps run by a virtual user.
type Scenario struct {
	Name   string `yaml:"name"`
	Weight int    `yaml:"weight,omitempty"`
	Steps  []Step `yaml:"steps"`
}
