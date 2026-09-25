// Package chaos wraps the Toxiproxy Go client with barrage-specific helpers.
//
// Toxiproxy itself is a separate process (toxiproxy-server) that must be
// running alongside barrage during a chaos run. This package only speaks to
// its HTTP API on localhost:8474 by default — it never spawns the server.
//
// Toxiproxy is protocol-blind: it relays raw TCP bytes regardless of what is
// inside them (Postgres wire protocol, RESP, HTTP). The only thing that
// changes per runner is which host:port it dials.
package chaos

import (
	"fmt"
	"strings"

	toxiproxy "github.com/Shopify/toxiproxy/v2/client"
)

// DefaultAPIAddr is the Toxiproxy HTTP API address used when none is given.
const DefaultAPIAddr = "localhost:8474"

// SupportedToxics is the set of toxic types barrage exposes. It mirrors what
// Toxiproxy natively supports — barrage does not invent custom fault types.
// "down" is special: Toxiproxy implements it as proxy disable/enable, not as
// a toxic, so the manager translates it to Disable/Enable calls.
var SupportedToxics = map[string]bool{
	"latency":     true,
	"bandwidth":   true,
	"timeout":     true,
	"slow_close":  true,
	"reset_peer":  true,
	"slicer":      true,
	"limit_data":  true,
	"packet_loss": true,
	"down":        true,
}

// Manager speaks to one running Toxiproxy instance.
type Manager struct {
	client  *toxiproxy.Client
	apiAddr string
}

// NewManager connects to a running Toxiproxy instance at apiAddr.
// An empty apiAddr selects DefaultAPIAddr. It pings the server so a missing
// or unreachable Toxiproxy fails fast instead of mid-run.
func NewManager(apiAddr string) (*Manager, error) {
	if strings.TrimSpace(apiAddr) == "" {
		apiAddr = DefaultAPIAddr
	}
	client := toxiproxy.NewClient(apiAddr)
	m := &Manager{client: client, apiAddr: apiAddr}
	if _, err := client.Version(); err != nil {
		return nil, fmt.Errorf("chaos: cannot reach toxiproxy at %s: %w", apiAddr, err)
	}
	return m, nil
}

// APIAddr reports the address this manager talks to.
func (m *Manager) APIAddr() string {
	return m.apiAddr
}

// CreateProxy creates a proxy that listens on listenAddr and forwards to
// upstreamAddr. If a proxy with the same name already exists it is replaced
// so reruns are idempotent.
func (m *Manager) CreateProxy(name, listenAddr, upstreamAddr string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("chaos: proxy name must not be empty")
	}
	if strings.TrimSpace(listenAddr) == "" {
		return fmt.Errorf("chaos: proxy %q listen address must not be empty", name)
	}
	if strings.TrimSpace(upstreamAddr) == "" {
		return fmt.Errorf("chaos: proxy %q upstream address must not be empty", name)
	}
	// Populate replaces an existing proxy with the same name when listen or
	// upstream differ, and leaves it untouched when they match — exactly the
	// idempotent create-or-replace we want for repeated runs.
	_, err := m.client.Populate([]toxiproxy.Proxy{{
		Name:     name,
		Listen:   listenAddr,
		Upstream: upstreamAddr,
		Enabled:  true,
	}})
	if err != nil {
		return fmt.Errorf("chaos: create proxy %q: %w", name, err)
	}
	return nil
}

// AddToxic adds a toxic to a proxy with default stream (downstream) and full
// toxicity (1.0). Attrs carries the toxic-specific parameters, e.g.
// {"latency": 500} for 500ms of latency.
func (m *Manager) AddToxic(proxyName, toxicName, toxicType string, attrs map[string]any) error {
	return m.AddToxicFull(proxyName, toxicName, toxicType, "downstream", 1.0, attrs)
}

// AddToxicFull adds a toxic with explicit stream and toxicity.
// An empty stream defaults to downstream; a toxicity of 0 selects 1.0 (full).
// An empty toxicName lets Toxiproxy default it to <type>_<stream>.
func (m *Manager) AddToxicFull(proxyName, toxicName, toxicType, stream string, toxicity float32, attrs map[string]any) error {
	if strings.TrimSpace(proxyName) == "" {
		return fmt.Errorf("chaos: proxy name must not be empty")
	}
	if !SupportedToxics[toxicType] {
		return fmt.Errorf("chaos: unsupported toxic type %q", toxicType)
	}
	// "down" is not a toxic in Toxiproxy — it disables the proxy.
	if toxicType == "down" {
		return m.DisableProxy(proxyName)
	}
	if strings.TrimSpace(stream) == "" {
		stream = "downstream"
	}
	if stream != "upstream" && stream != "downstream" {
		return fmt.Errorf("chaos: stream must be upstream or downstream, got %q", stream)
	}
	if toxicity == 0 {
		toxicity = 1.0
	}
	converted := make(toxiproxy.Attributes, len(attrs))
	for k, v := range attrs {
		converted[k] = v
	}
	_, err := m.client.AddToxic(&toxiproxy.ToxicOptions{
		ProxyName:  proxyName,
		ToxicName:  toxicName,
		ToxicType:  toxicType,
		Stream:     stream,
		Toxicity:   toxicity,
		Attributes: converted,
	})
	if err != nil {
		return fmt.Errorf("chaos: add toxic %q to proxy %q: %w", toxicType, proxyName, err)
	}
	return nil
}

// RemoveToxic removes a toxic from a proxy. Removing a "down" fault
// re-enables the proxy.
func (m *Manager) RemoveToxic(proxyName, toxicName string) error {
	if strings.TrimSpace(proxyName) == "" {
		return fmt.Errorf("chaos: proxy name must not be empty")
	}
	if strings.TrimSpace(toxicName) == "" {
		return fmt.Errorf("chaos: toxic name must not be empty")
	}
	// A "down" fault has no toxic entry — it disabled the proxy.
	if toxicName == "down" || strings.HasPrefix(toxicName, "down") {
		// Best effort: try removal first (in case it really is a toxic),
		// then ensure the proxy is enabled.
		_ = m.client.RemoveToxic(&toxiproxy.ToxicOptions{
			ProxyName: proxyName,
			ToxicName: toxicName,
		})
		return m.EnableProxy(proxyName)
	}
	err := m.client.RemoveToxic(&toxiproxy.ToxicOptions{
		ProxyName: proxyName,
		ToxicName: toxicName,
	})
	if err != nil {
		return fmt.Errorf("chaos: remove toxic %q from proxy %q: %w", toxicName, proxyName, err)
	}
	return nil
}

// DisableProxy takes a proxy down: no connections pass through and active
// connections drop.
func (m *Manager) DisableProxy(proxyName string) error {
	proxy, err := m.client.Proxy(proxyName)
	if err != nil {
		return fmt.Errorf("chaos: get proxy %q: %w", proxyName, err)
	}
	if err := proxy.Disable(); err != nil {
		return fmt.Errorf("chaos: disable proxy %q: %w", proxyName, err)
	}
	return nil
}

// EnableProxy brings a disabled proxy back up.
func (m *Manager) EnableProxy(proxyName string) error {
	proxy, err := m.client.Proxy(proxyName)
	if err != nil {
		return fmt.Errorf("chaos: get proxy %q: %w", proxyName, err)
	}
	if err := proxy.Enable(); err != nil {
		return fmt.Errorf("chaos: enable proxy %q: %w", proxyName, err)
	}
	return nil
}

// TeardownProxy deletes a proxy and closes all connections through it.
func (m *Manager) TeardownProxy(proxyName string) error {
	if strings.TrimSpace(proxyName) == "" {
		return fmt.Errorf("chaos: proxy name must not be empty")
	}
	proxy, err := m.client.Proxy(proxyName)
	if err != nil {
		// Already gone — teardown is idempotent.
		return nil
	}
	if err := proxy.Delete(); err != nil {
		return fmt.Errorf("chaos: delete proxy %q: %w", proxyName, err)
	}
	return nil
}

// ToxicInfo is one active toxic on a proxy.
type ToxicInfo struct {
	Name       string
	Type       string
	Stream     string
	Toxicity   float32
	Attributes map[string]any
}

// ListToxics returns the active toxics on a proxy.
func (m *Manager) ListToxics(proxyName string) ([]ToxicInfo, error) {
	proxy, err := m.client.Proxy(proxyName)
	if err != nil {
		return nil, fmt.Errorf("chaos: get proxy %q: %w", proxyName, err)
	}
	toxics, err := proxy.Toxics()
	if err != nil {
		return nil, fmt.Errorf("chaos: list toxics on proxy %q: %w", proxyName, err)
	}
	out := make([]ToxicInfo, 0, len(toxics))
	for _, t := range toxics {
		attrs := make(map[string]any, len(t.Attributes))
		for k, v := range t.Attributes {
			attrs[k] = v
		}
		out = append(out, ToxicInfo{
			Name:       t.Name,
			Type:       t.Type,
			Stream:     t.Stream,
			Toxicity:   t.Toxicity,
			Attributes: attrs,
		})
	}
	return out, nil
}

// HasToxic reports whether a toxic with the given name is active on a proxy.
func (m *Manager) HasToxic(proxyName, toxicName string) (bool, error) {
	toxics, err := m.ListToxics(proxyName)
	if err != nil {
		return false, err
	}
	for _, t := range toxics {
		if t.Name == toxicName {
			return true, nil
		}
	}
	return false, nil
}

// Reset enables all proxies and removes all active toxics. It is the cleanup
// path after a chaos run.
func (m *Manager) Reset() error {
	if err := m.client.ResetState(); err != nil {
		return fmt.Errorf("chaos: reset state: %w", err)
	}
	return nil
}
