// Integration test for the chaos manager against a real local Toxiproxy.
//
// These tests prove the real wiring works — not a mock. They are skipped
// when Toxiproxy is not reachable at localhost:8474 so normal runs never
// fail for lack of the external binary. Start it with:
//
//	toxiproxy-server &
package chaos

import (
	"io"
	"net"
	"testing"
	"time"
)

// startEchoServer runs a TCP echo server on 127.0.0.1:0 and returns its
// address. Each connection echoes every byte back until closed.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if _, werr := c.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// freeAddr returns an unused 127.0.0.1:port address by listening and closing.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free addr: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// roundTrip dials addr, writes a byte, and measures the echo latency.
func roundTrip(t *testing.T, addr string) time.Duration {
	t.Helper()
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	return time.Since(start)
}

// requireManager skips the test when Toxiproxy is not running locally.
func requireManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(DefaultAPIAddr)
	if err != nil {
		t.Skipf("toxiproxy not reachable at %s: %v", DefaultAPIAddr, err)
	}
	return m
}

func TestManagerLatencyToxic(t *testing.T) {
	m := requireManager(t)
	upstream := startEchoServer(t)
	listen := freeAddr(t)

	const proxy = "barrage-test-echo"
	if err := m.CreateProxy(proxy, listen, upstream); err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	t.Cleanup(func() { _ = m.TeardownProxy(proxy) })

	// Baseline through the idle proxy: must behave like a direct connection.
	base := roundTrip(t, listen)
	if base > 500*time.Millisecond {
		t.Fatalf("idle proxy round-trip = %v, want < 500ms", base)
	}

	// Add 500ms downstream latency and prove the round-trip slows down.
	if err := m.AddToxic(proxy, "latency_downstream", "latency", map[string]any{
		"latency": 500,
	}); err != nil {
		t.Fatalf("add latency toxic: %v", err)
	}
	slow := roundTrip(t, listen)
	if slow < 400*time.Millisecond {
		t.Errorf("toxic round-trip = %v, want >= 400ms (500ms toxic)", slow)
	}

	// Removal must restore the fast path.
	if err := m.RemoveToxic(proxy, "latency_downstream"); err != nil {
		t.Fatalf("remove toxic: %v", err)
	}
	fast := roundTrip(t, listen)
	if fast > 500*time.Millisecond {
		t.Errorf("post-removal round-trip = %v, want < 500ms", fast)
	}
}

func TestManagerHasToxic(t *testing.T) {
	m := requireManager(t)
	upstream := startEchoServer(t)
	listen := freeAddr(t)

	const proxy = "barrage-test-has-toxic"
	if err := m.CreateProxy(proxy, listen, upstream); err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	t.Cleanup(func() { _ = m.TeardownProxy(proxy) })

	has, err := m.HasToxic(proxy, "latency_downstream")
	if err != nil {
		t.Fatalf("has toxic: %v", err)
	}
	if has {
		t.Fatal("unexpected toxic before add")
	}
	if err := m.AddToxic(proxy, "latency_downstream", "latency", map[string]any{"latency": 100}); err != nil {
		t.Fatalf("add toxic: %v", err)
	}
	has, err = m.HasToxic(proxy, "latency_downstream")
	if err != nil {
		t.Fatalf("has toxic: %v", err)
	}
	if !has {
		t.Fatal("toxic missing after add")
	}
	if err := m.RemoveToxic(proxy, "latency_downstream"); err != nil {
		t.Fatalf("remove toxic: %v", err)
	}
	has, err = m.HasToxic(proxy, "latency_downstream")
	if err != nil {
		t.Fatalf("has toxic: %v", err)
	}
	if has {
		t.Fatal("toxic still present after remove")
	}
}
