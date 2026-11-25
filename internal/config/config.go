// Package config implements the sandbox.yaml declaration surface (I6: the
// user-configurable layer of every override chain) — machines, work_home
// dual-path (§13.1), DNS network, and limit overrides.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Storage is the sandboxd-side work_home view.
type Storage struct {
	WorkHome string `yaml:"work_home"`
}

// Docker groups docker-specific deployment settings (§13 dual-path + DNS).
type Docker struct {
	WorkHomeHost  string `yaml:"work_home_host"` // daemon-side host path
	Network       string `yaml:"network"`        // user-defined bridge (container-name DNS)
	Containerized bool   `yaml:"containerized"`  // sandboxd itself runs in a container
}

// Target mirrors remote.Target for YAML declaration (AD-026: registration is
// declarative, zero tools). Kind names the axis the target lives on — P0 is
// "ssh"; a k8s cluster is also a machine, but a different axis (the status
// payload reports axes: docker / ssh / ...).
type Target struct {
	Kind   string `yaml:"kind,omitempty"` // default: ssh
	Host   string `yaml:"host"`
	Port   int    `yaml:"port,omitempty"`
	User   string `yaml:"user,omitempty"`
	Key    string `yaml:"key,omitempty"`
	OSType string `yaml:"os_type"`
}

// Axis returns the target's axis name (default "ssh").
func (t Target) Axis() string {
	if t.Kind == "" {
		return "ssh"
	}
	return t.Kind
}

// Limits are the user-configurable overrides (contract defaults live in the
// consuming packages' DEFAULT_* constants — I6 single-hit resolution happens
// in Resolve* methods).
type Limits struct {
	ExecTimeoutSeconds int `yaml:"exec_timeout_seconds,omitempty"`
}

// Config is the whole sandbox.yaml.
type Config struct {
	Storage  Storage          `yaml:"storage"`
	Docker   Docker           `yaml:"docker"`
	Machines map[string]Target `yaml:"machines"`
	Limits   Limits           `yaml:"limits,omitempty"`

	path string
}

// Load parses sandbox.yaml. Missing file is an error (fail-fast posture).
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.path = path
	return &cfg, nil
}

// ResolveExecTimeout: user override ?: contract default (60s, spec §4.2).
// Single-hit resolution — one expression, no scattered fallbacks (I6.1).
func (c *Config) ResolveExecTimeout() time.Duration {
	if c.Limits.ExecTimeoutSeconds > 0 {
		return time.Duration(c.Limits.ExecTimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}

// Validate enforces deployment invariants before any work starts (§13.1
// fail-fast): containerized deployment without the daemon-side path is a
// configuration error caught at startup, not at first build.
func (c *Config) Validate() error {
	if c.Storage.WorkHome == "" {
		return fmt.Errorf("storage.work_home is required (workdir invariant)")
	}
	if c.Docker.Containerized && c.Docker.WorkHomeHost == "" {
		return fmt.Errorf("containerized deployment requires docker.work_home_host " +
			"(daemon must see the host path; §13.1)")
	}
	for name, m := range c.Machines {
		if m.Host == "" {
			return fmt.Errorf("machine %q: host is required", name)
		}
		if m.OSType != "linux" && m.OSType != "windows" {
			return fmt.Errorf("machine %q: os_type must be linux|windows (got %q)", name, m.OSType)
		}
	}
	return nil
}

// HostWorkHome returns the daemon-side view (bare metal: same as storage).
func (c *Config) HostWorkHome() string {
	if c.Docker.WorkHomeHost != "" {
		return c.Docker.WorkHomeHost
	}
	return c.Storage.WorkHome
}
