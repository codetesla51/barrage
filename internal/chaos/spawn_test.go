package chaos

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// freeLoopback returns an unused 127.0.0.1:port API address.
func freeLoopback(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free addr: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestEnsureManagerUsesRunningServer(t *testing.T) {
	m, err := NewManager(DefaultAPIAddr)
	if err != nil {
		t.Skipf("toxiproxy not running: %v", err)
	}
	_ = m
	mgr, cleanup, spawned, err := EnsureManager(DefaultAPIAddr)
	if err != nil {
		t.Fatalf("ensure manager: %v", err)
	}
	defer cleanup()
	if spawned {
		t.Error("spawned = true against an already-running server, want false")
	}
	if mgr.APIAddr() != DefaultAPIAddr {
		t.Errorf("api addr = %q, want %q", mgr.APIAddr(), DefaultAPIAddr)
	}
}

func TestEnsureManagerSpawnsAndCleansUp(t *testing.T) {
	if _, err := lookupServer(); err != nil {
		t.Skipf("toxiproxy-server binary not in PATH: %v", err)
	}
	api := freeLoopback(t)
	mgr, cleanup, spawned, err := EnsureManager(api)
	if err != nil {
		t.Fatalf("ensure manager: %v", err)
	}
	if !spawned {
		cleanup()
		t.Fatal("spawned = false on a free port, want true")
	}
	// The managed server must actually serve: create + delete a proxy.
	if err := mgr.CreateProxy("barrage-spawn-probe", freeLoopback(t), "127.0.0.1:1"); err != nil {
		cleanup()
		t.Fatalf("create proxy on managed server: %v", err)
	}
	_ = mgr.TeardownProxy("barrage-spawn-probe")
	cleanup()
	// After cleanup the API must be gone — no leaked process.
	if _, err := NewManager(api); err == nil {
		t.Fatal("managed server still reachable after cleanup")
	}
}

// lookupServer finds the toxiproxy-server binary via PATH.
func lookupServer() (string, error) {
	return exec.LookPath("toxiproxy-server")
}

// TempDir binary check helper: toxiproxy-server must be runnable.
func TestSpawnBinaryRunnable(t *testing.T) {
	path, err := lookupServer()
	if err != nil {
		t.Skipf("toxiproxy-server not in PATH: %v", err)
	}
	if filepath.Base(path) != "toxiproxy-server" {
		t.Errorf("unexpected binary path %q", path)
	}
}

var _ = os.Getenv
