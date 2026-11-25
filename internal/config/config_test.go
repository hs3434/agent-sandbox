package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFull(t *testing.T) {
	yaml := `
storage:
  work_home: /srv/work_home
docker:
  work_home_host: /srv/agent-sandbox/work_home
  network: agent-sandbox
machines:
  build-1:
    host: 192.168.1.20
    user: deploy
    key: ~/.ssh/id_ed25519
    os_type: linux
  win-1:
    host: 192.168.1.30
    user: admin
    os_type: windows
limits:
  exec_timeout_seconds: 120
`
	p := filepath.Join(t.TempDir(), "sandbox.yaml")
	os.WriteFile(p, []byte(yaml), 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Storage.WorkHome != "/srv/work_home" {
		t.Fatalf("work_home = %q", cfg.Storage.WorkHome)
	}
	if cfg.Docker.WorkHomeHost != "/srv/agent-sandbox/work_home" {
		t.Fatalf("work_home_host = %q", cfg.Docker.WorkHomeHost)
	}
	if cfg.Docker.Network != "agent-sandbox" {
		t.Fatalf("network = %q", cfg.Docker.Network)
	}
	if len(cfg.Machines) != 2 {
		t.Fatalf("machines = %d", len(cfg.Machines))
	}
	m := cfg.Machines["build-1"]
	if m.Host != "192.168.1.20" || m.OSType != "linux" {
		t.Fatalf("build-1 = %+v", m)
	}
	// I6: declared default, override applied
	if cfg.Limits.ExecTimeoutSeconds != 120 {
		t.Fatalf("exec_timeout = %d", cfg.Limits.ExecTimeoutSeconds)
	}
}

func TestDeclaredDefaults(t *testing.T) {
	// I6 chain: user-configurable ? contract defaults ? implementation fallback
	cfg := &Config{}
	if got := cfg.ResolveExecTimeout(); got != 60*time.Second {
		t.Fatalf("default exec timeout = %v, want 60s", got)
	}
	cfg.Limits.ExecTimeoutSeconds = 300
	if got := cfg.ResolveExecTimeout(); got != 300*time.Second {
		t.Fatalf("override ignored: %v", got)
	}
	if cfg.Storage.WorkHome != "" {
		t.Fatal("zero value must be empty")
	}
}

func TestFailFastContainerizedWithoutHostPath(t *testing.T) {
	// §13.1: containerized deployment + missing work_home_host = refuse to start
	cfg := &Config{Storage: Storage{WorkHome: "/work_home"}, Docker: Docker{}}
	cfg.Docker.Containerized = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("containerized without work_home_host must fail fast")
	}
	cfg.Docker.WorkHomeHost = "/srv/work_home"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid containerized config rejected: %v", err)
	}
}

func TestValidateBareMetal(t *testing.T) {
	cfg := &Config{Storage: Storage{WorkHome: "/srv/work_home"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("bare metal (single path) must pass: %v", err)
	}
	// missing work_home anywhere = fail
	cfg2 := &Config{}
	if err := cfg2.Validate(); err == nil {
		t.Fatal("missing work_home must fail")
	}
}
