package barrage

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codetesla51/barrage/internal/chaos"
)

// ChaosProxyConfig describes one Toxiproxy proxy: listen forwards to upstream.
// Example: name db-proxy, listen 127.0.0.1:26000, upstream 127.0.0.1:5432.
// Runners dial the listen address when chaos mode is on.
type ChaosProxyConfig struct {
	Name     string `yaml:"name"`
	Listen   string `yaml:"listen"`
	Upstream string `yaml:"upstream"`
}

// ChaosFaultConfig is one scheduled fault event in YAML form. At is the offset
// from run start; Duration is how long the toxic stays active (0 leaves it
// until the run ends, when the scheduler cleans it up). Proxy names a proxy
// from the proxies list; Type is a Toxiproxy toxic type; Attrs carries the
// toxic-specific parameters. Name, Stream, and Toxicity are optional.
type ChaosFaultConfig struct {
	At       Duration       `yaml:"at"`
	Duration Duration       `yaml:"duration"`
	Proxy    string         `yaml:"proxy"`
	Type     string         `yaml:"type"`
	Name     string         `yaml:"name,omitempty"`
	Stream   string         `yaml:"stream,omitempty"`
	Toxicity float32        `yaml:"toxicity,omitempty"`
	Attrs    map[string]any `yaml:"attrs,omitempty"`
}

// FaultEvent is the runtime form of a scheduled fault. It mirrors
// ChaosFaultConfig with plain durations so tests can build it without YAML.
type FaultEvent struct {
	At        time.Duration
	Duration  time.Duration
	Proxy     string
	ToxicType string
	ToxicName string
	Stream    string
	Toxicity  float32
	Attrs     map[string]any
}

// ChaosConfig is the top-level chaos: block. API is the Toxiproxy HTTP API
// address (default localhost:8474). Proxies are created at run start;
// Faults fire during the run.
type ChaosConfig struct {
	API     string             `yaml:"api,omitempty"`
	Proxies []ChaosProxyConfig `yaml:"proxies,omitempty"`
	Faults  []ChaosFaultConfig `yaml:"faults,omitempty"`
}

// ChaosEvent is one logged fault injection or removal, timestamped so the
// correlation engine can plot it against latency/error buckets on the same
// timeline. Offset is from run start; At is wall time.
type ChaosEvent struct {
	Offset time.Duration
	At     time.Time
	Proxy  string
	Toxic  string
	Type   string
	Action string // "add" or "remove"
}

// faultEvents converts YAML fault configs to runtime events.
func faultEvents(cfgs []ChaosFaultConfig) []FaultEvent {
	out := make([]FaultEvent, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, FaultEvent{
			At:        time.Duration(c.At),
			Duration:  time.Duration(c.Duration),
			Proxy:     c.Proxy,
			ToxicType: c.Type,
			ToxicName: c.Name,
			Stream:    c.Stream,
			Toxicity:  c.Toxicity,
			Attrs:     c.Attrs,
		})
	}
	return out
}

// effectiveToxicName resolves the toxic name: explicit Name wins, otherwise
// Toxiproxy's default <type>_<stream>.
func effectiveToxicName(ev FaultEvent) string {
	if strings.TrimSpace(ev.ToxicName) != "" {
		return ev.ToxicName
	}
	stream := ev.Stream
	if strings.TrimSpace(stream) == "" {
		stream = "downstream"
	}
	return ev.ToxicType + "_" + stream
}

// validateChaos checks a chaos config without touching the network.
func validateChaos(c *ChaosConfig, runDuration time.Duration) error {
	if c == nil {
		return nil
	}
	names := make(map[string]bool, len(c.Proxies))
	for i, p := range c.Proxies {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("chaos proxies[%d]: name must not be empty", i)
		}
		if names[p.Name] {
			return fmt.Errorf("chaos proxies[%d]: duplicate proxy name %q", i, p.Name)
		}
		names[p.Name] = true
		if strings.TrimSpace(p.Listen) == "" {
			return fmt.Errorf("chaos proxies[%d] %q: listen must not be empty", i, p.Name)
		}
		if strings.TrimSpace(p.Upstream) == "" {
			return fmt.Errorf("chaos proxies[%d] %q: upstream must not be empty", i, p.Name)
		}
	}
	for i, f := range c.Faults {
		if strings.TrimSpace(f.Proxy) == "" {
			return fmt.Errorf("chaos faults[%d]: proxy must not be empty", i)
		}
		if len(names) > 0 && !names[f.Proxy] {
			return fmt.Errorf("chaos faults[%d]: unknown proxy %q", i, f.Proxy)
		}
		if !chaos.SupportedToxics[f.Type] {
			return fmt.Errorf("chaos faults[%d]: unsupported toxic type %q", i, f.Type)
		}
		if time.Duration(f.At) < 0 {
			return fmt.Errorf("chaos faults[%d]: at must not be negative", i)
		}
		if time.Duration(f.Duration) < 0 {
			return fmt.Errorf("chaos faults[%d]: duration must not be negative", i)
		}
		if runDuration > 0 && time.Duration(f.At) > runDuration {
			return fmt.Errorf("chaos faults[%d]: at %s exceeds run duration %s", i, time.Duration(f.At), runDuration)
		}
		if f.Stream != "" && f.Stream != "upstream" && f.Stream != "downstream" {
			return fmt.Errorf("chaos faults[%d]: stream must be upstream or downstream, got %q", i, f.Stream)
		}
		if f.Toxicity < 0 || f.Toxicity > 1 {
			return fmt.Errorf("chaos faults[%d]: toxicity must be in [0,1], got %v", i, f.Toxicity)
		}
	}
	return nil
}

// toxicController is the subset of *chaos.Manager the scheduler needs. It
// exists so tests can substitute a fake without a real Toxiproxy.
type toxicController interface {
	AddToxicFull(proxyName, toxicName, toxicType, stream string, toxicity float32, attrs map[string]any) error
	RemoveToxic(proxyName, toxicName string) error
}

// Scheduler fires FaultEvents at their offsets during a run via the
// Toxiproxy client. Every Add/Remove is logged with a timestamp in the same
// stderr channel barrage already uses for run progress.
type Scheduler struct {
	ctrl   toxicController
	events []FaultEvent
	start  time.Time

	mu  sync.Mutex
	log []ChaosEvent
}

// NewScheduler builds a scheduler for events anchored at start.
func NewScheduler(ctrl toxicController, events []FaultEvent, start time.Time) *Scheduler {
	sorted := append([]FaultEvent(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At < sorted[j].At })
	return &Scheduler{ctrl: ctrl, events: sorted, start: start}
}

// Events returns the logged chaos events so far (a copy).
func (s *Scheduler) Events() []ChaosEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ChaosEvent(nil), s.log...)
}

// record appends one event and prints it to stderr like the run logger.
func (s *Scheduler) record(offset time.Duration, at time.Time, proxy, toxic, typ, action string) {
	s.mu.Lock()
	s.log = append(s.log, ChaosEvent{Offset: offset, At: at, Proxy: proxy, Toxic: toxic, Type: typ, Action: action})
	s.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[chaos] %s %s on %s at %s\n", action, typ, proxy, offset.Round(100*time.Millisecond))
}

// Run blocks until ctx ends, firing Add at each event's offset and Remove
// Duration later (0 duration stays until ctx ends, then is cleaned up).
// It returns the full event log.
func (s *Scheduler) Run(ctx context.Context) []ChaosEvent {
	if s.ctrl == nil {
		return s.Events()
	}
	type active struct {
		proxy, toxic, typ string
		removeAt          time.Time
		hasRemove         bool
	}
	var actives []active

	// Fire adds in offset order; removals are handled by timers below.
	for _, ev := range s.events {
		name := effectiveToxicName(ev)
		stream := ev.Stream
		if strings.TrimSpace(stream) == "" {
			stream = "downstream"
		}
		toxicity := ev.Toxicity
		if toxicity == 0 {
			toxicity = 1.0
		}
		target := s.start.Add(ev.At)
		select {
		case <-ctx.Done():
			goto cleanup
		case <-time.After(time.Until(target)):
		}
		if err := s.ctrl.AddToxicFull(ev.Proxy, name, ev.ToxicType, stream, toxicity, ev.Attrs); err != nil {
			// A failed injection is still logged so the report shows the gap
			// instead of silently pretending the window ran.
			s.record(time.Since(s.start), time.Now(), ev.Proxy, name, ev.ToxicType, "add_failed")
			continue
		}
		s.record(ev.At, time.Now(), ev.Proxy, name, ev.ToxicType, "add")
		if ev.Duration > 0 {
			a := active{proxy: ev.Proxy, toxic: name, typ: ev.ToxicType, removeAt: target.Add(ev.Duration), hasRemove: true}
			actives = append(actives, a)
			// Schedule the removal without blocking later adds.
			go func(proxy, toxic, typ string, at time.Duration, removeAt time.Time) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Until(removeAt)):
				}
				if err := s.ctrl.RemoveToxic(proxy, toxic); err != nil {
					return
				}
				s.record(at, time.Now(), proxy, toxic, typ, "remove")
			}(ev.Proxy, name, ev.ToxicType, ev.At+ev.Duration, a.removeAt)
		} else {
			actives = append(actives, active{proxy: ev.Proxy, toxic: name, typ: ev.ToxicType})
		}
	}

	// Wait for the run to end so Duration windows and manual toxics clean up.
	<-ctx.Done()

cleanup:
	// Best-effort cleanup: remove anything still active so toxics never leak
	// into the next run. Already-removed timed toxics fail here and are
	// skipped (their "remove" was logged by the timer above).
	for _, a := range actives {
		if a.hasRemove && time.Now().Before(a.removeAt) {
			if err := s.ctrl.RemoveToxic(a.proxy, a.toxic); err == nil {
				s.record(time.Since(s.start), time.Now(), a.proxy, a.toxic, a.typ, "remove")
			}
			continue
		}
		if !a.hasRemove {
			if err := s.ctrl.RemoveToxic(a.proxy, a.toxic); err == nil {
				s.record(time.Since(s.start), time.Now(), a.proxy, a.toxic, a.typ, "remove")
			}
		}
	}
	return s.Events()
}

// effectiveDBConn returns the connection string the DB runner should dial:
// the chaos override when chaos mode is on and set, otherwise the real conn.
func effectiveDBConn(target DBTarget, chaosActive bool) string {
	if chaosActive && strings.TrimSpace(target.ChaosConn) != "" {
		return target.ChaosConn
	}
	return target.Conn
}

// effectiveRedisAddr returns the address the Redis runner should dial.
func effectiveRedisAddr(target RedisTarget, chaosActive bool) string {
	if chaosActive && strings.TrimSpace(target.ChaosAddr) != "" {
		return target.ChaosAddr
	}
	return target.Addr
}

// effectiveHTTPURL returns the URL the HTTP runner should hit.
func effectiveHTTPURL(target HTTPTarget, chaosActive bool) string {
	if chaosActive && strings.TrimSpace(target.ChaosURL) != "" {
		return target.ChaosURL
	}
	return target.URL
}

// effectiveStepURL returns the URL a scenario step should hit.
func effectiveStepURL(step Step, chaosActive bool) string {
	if chaosActive && strings.TrimSpace(step.ChaosURL) != "" {
		return step.ChaosURL
	}
	return step.URL
}
