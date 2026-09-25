package chaos

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// EnsureManager returns a Manager for apiAddr, starting a managed
// toxiproxy-server when none is running.
//
// When Toxiproxy is already reachable the returned cleanup is a no-op and
// spawned is false — barrage leaves the user's server alone. When barrage
// starts the server itself, cleanup stops it and spawned is true; the caller
// must defer cleanup so no proxy process leaks after the run.
//
// Every spawn and stop is announced on stderr so it is never silent.
func EnsureManager(apiAddr string) (mgr *Manager, cleanup func(), spawned bool, err error) {
	normalized := normalizeAPIAddr(apiAddr)
	if m, err := NewManager(normalized); err == nil {
		return m, func() {}, false, nil
	}

	bin, err := exec.LookPath("toxiproxy-server")
	if err != nil {
		return nil, nil, false, fmt.Errorf(
			"chaos: no toxiproxy at %s and no toxiproxy-server binary in PATH (install from https://github.com/Shopify/toxiproxy/releases or start it manually)",
			normalized)
	}

	host, port := splitAPIAddr(normalized)
	fmt.Fprintf(os.Stderr, "[chaos] toxiproxy not running at %s, starting %s ...\n", normalized, bin)
	cmd := exec.Command(bin, "-host", host, "-port", port)
	// Keep server logs out of the run output; failures surface via the
	// readiness poll below, not interleaved bytes on stderr.
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, nil, false, fmt.Errorf("chaos: start toxiproxy-server: %w", err)
	}

	// Wait for the API to answer, then connect. If it never does, kill the
	// child so a half-started server never leaks.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if m, err := NewManager(normalized); err == nil {
			fmt.Fprintf(os.Stderr, "[chaos] using managed toxiproxy-server (pid %d), will stop it after the run\n", cmd.Process.Pid)
			cleanup := func() {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
				fmt.Fprintln(os.Stderr, "[chaos] stopped managed toxiproxy-server")
			}
			return m, cleanup, true, nil
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			return nil, nil, false, fmt.Errorf(
				"chaos: toxiproxy-server started but API at %s never became ready (port in use?)", normalized)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// normalizeAPIAddr strips a URL scheme and defaults an empty address.
func normalizeAPIAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return DefaultAPIAddr
	}
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimSuffix(addr, "/")
	if addr == "" {
		return DefaultAPIAddr
	}
	return addr
}

// splitAPIAddr splits host:port for the server flags, defaulting to
// localhost:8474 when parts are missing.
func splitAPIAddr(addr string) (host, port string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port present: the whole value is the host.
		host = strings.Trim(addr, "[]")
		port = "8474"
	}
	if strings.TrimSpace(host) == "" {
		host = "localhost"
	}
	if strings.TrimSpace(port) == "" {
		port = "8474"
	}
	return host, port
}
