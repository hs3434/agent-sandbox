// sandboxd is the composition root (I3): the only place concrete backends,
// config, and the MCP face meet. Startup is fail-fast — configuration
// errors abort before any work (§13.1).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/hs3434/agent-sandbox/internal/config"
	"github.com/hs3434/agent-sandbox/internal/mcpserver"
	"github.com/hs3434/agent-sandbox/internal/orchestrator"
	"github.com/hs3434/agent-sandbox/pkg/audit"
	"github.com/hs3434/agent-sandbox/pkg/container"
	"github.com/hs3434/agent-sandbox/pkg/remote"
	"github.com/hs3434/agent-sandbox/pkg/store"
)

func main() {
	cfgPath := flag.String("config", "sandbox.yaml", "path to sandbox.yaml")
	flag.Parse()

	log.SetFlags(log.Ltime)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal("config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		fatal("config invalid (fail-fast): %v", err)
	}

	// state + audit stores
	dataDir := filepath.Join(cfg.Storage.WorkHome, ".sandboxd")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fatal("data dir: %v", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "state.db"))
	if err != nil {
		fatal("store: %v", err)
	}
	defer st.Close()
	sink, err := audit.OpenSQLite(filepath.Join(dataDir, "audit.db"))
	if err != nil {
		fatal("audit: %v", err)
	}
	defer sink.Close()
	bus := audit.NewBus(sink)

	// docker backend (default route)
	// dual-path (§13.1): the daemon must see the host path when sandboxd
	// itself runs containerized
	dockerBe, err := container.Open(container.Options{
		WorkHome: cfg.HostWorkHome(),
		Network:  cfg.Docker.Network,
		Tenant:   "default",
	})
	if err != nil {
		fatal("docker backend: %v", err)
	}
	defer dockerBe.Close()

	// ssh machines (declarative, AD-026): connect each on startup
	namedBackends := map[string]any{}
	if len(cfg.Machines) > 0 {
		remoteBe, err := remote.Open()
		if err != nil {
			fatal("remote backend: %v", err)
		}
		defer remoteBe.Close()
		for name, m := range cfg.Machines {
			t := remote.Target{Name: name, Host: m.Host, Port: m.Port,
				User: m.User, Key: m.Key, OSType: m.OSType}
			if err := remoteBe.Connect(t); err != nil {
				log.Printf("machine %q connect failed (will retry lazily): %v", name, err)
			}
			rm := remoteMachine{be: remoteBe, name: name, axis: m.Axis()}
			rm.StartCompanion("default") // D-18 owner id (config surface in T10)
			namedBackends[name] = &rm    // pointer: CompanionState has a pointer receiver
		}
	}

	// reconcile: adopt daemon-truth before serving (spec §3.1)
	orch, err := orchestrator.New(orchestrator.Deps{
		Store:       st,
		Audit:       bus,
		Backend:     adapter{dockerBe},
		Backends:    namedBackends,
		WorkHome:    cfg.HostWorkHome(),
		Tenant:      "default",
		ExecTimeout: cfg.ResolveExecTimeout(),
		ImageLister: func() ([]string, error) { return dockerBe.ListImages(context.Background()) },
	})
	if err != nil {
		fatal("orchestrator: %v", err)
	}
	report, err := orch.Reconcile(context.Background())
	if err != nil {
		log.Printf("reconcile: %v (continuing; orphan handling is advisory)", err)
	} else {
		log.Printf("reconcile: ours=%d corrected=%d markedError=%d unmanaged=%d (not ours — untouched)",
			report.Ours, report.Adopted, report.MarkedError, len(report.Unmanaged))
	}

	// MCP face
	srv := mcpserver.New(mcpserver.Deps{
		Store:  st,
		Audit:  bus,
		Tenant: "default",
		Orch:   orchestratorFaceAdapter{orch},
	})
	log.Printf("sandboxd ready: work_home=%s machines=%d", cfg.Storage.WorkHome, len(cfg.Machines))
	if err := srv.Run(context.Background()); err != nil {
		fatal("mcp server: %v", err)
	}
}

func fatal(format string, args ...any) {
	log.Printf("FATAL: "+format, args...)
	os.Exit(1)
}

// adapters: concrete backends → orchestrator/mcpserver contracts (the only
// place implementation types meet contract types — I3/I7).

// adapter: container.Backend → orchestrator.lifecycleOps。嵌入暴露全部
// 数据面方法（Exec/ExecAsync/Kill/ReadFile/WriteFile/Build/ListImages），
// 签名直通无转换 — face 断言按能力逐个匹配。
type adapter struct{ *container.Backend }
