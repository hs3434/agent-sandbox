// Package mcpserver assembles the P0 tool face (17 tools / 5 families,
// spec §4): MCP tools delegate to the orchestrator/store/audit composition;
// every response carries piggyback body events (v3.17感知栈).
//
// This is the only layer the Agent sees; the composition root (cmd/sandboxd)
// is the only layer that wires concrete backends (I3).
package mcpserver

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hs3434/agent-sandbox/pkg/audit"
	"github.com/hs3434/agent-sandbox/pkg/resource"
	"github.com/hs3434/agent-sandbox/pkg/store"
)

// StateDocMax is the single-document memory size guard (bytes).
const StateDocMax = 64 * 1024

// Deps wires the server to the control plane (composition root supplies
// concrete implementations).
type Deps struct {
	Store      *store.Store
	Audit      *audit.Bus
	Tenant     string
	DefaultEnv string           // follow-style default (status.default is the truth source)
	Orch       OrchestratorFace // lifecycle/exec/file composition (nil = face-audit mode)
}

// Server holds the assembled tool face and its shared dependencies.
type Server struct {
	mu       sync.Mutex
	mcp      *mcp.Server
	deps     Deps
	stateDoc json.RawMessage
	names    []string
	evSeq    int64 // piggyback cursor: last delivered audit rowid (P0 single-client)
}

// New builds the server (face audit / tests may pass a Deps with nil Orch —
// tools report "backend not wired" honestly).
func New(opts Deps) *Server {
	s := &Server{deps: opts}
	s.build()
	return s
}

func (s *Server) build() {
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "sandboxd", Version: "0.1.0"}, nil)
	freeForm := func() *jsonschema.Schema {
		return &jsonschema.Schema{Type: "object"} // additionalProperties default true
	}
	add := func(name, desc string) {
		s.mcp.AddTool(&mcp.Tool{Name: name, Description: desc, InputSchema: freeForm()},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				out, err := s.route(ctx, name, req.Params.Arguments)
				if err != nil {
					return nil, err
				}
				b, err := json.Marshal(out)
				if err != nil {
					return nil, err
				}
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
				}, nil
			})
		s.names = append(s.names, name)
	}

	// —— container family (docker backend exclusive, AD-027) ——
	add("container_create", "Create a container body. Same name after remove = self-healing (workdir survives, audit marks resource_recreated). Args: name (required), image (optional — default busybox; use container_images to list), tags (optional map).")
	add("container_remove", "Remove a container. Default keeps the workdir (the name is an asset address); purge=true deletes it. Args: id (optional — default env), purge (optional bool).")
	add("container_build", "Build an image from a Dockerfile in the env workdir (declarative body workflow). Args: tag (required — explicit tags prevent collision), dockerfile (optional, default 'Dockerfile', workdir-relative).")
	add("container_images", "List locally available images (selection basis for container_create/container_build). No args.")

	// —— shell family ——
	add("shell_exec", "Execute a command. Blocking by default (returns exit_code/output; timeout 60s kills the process tree — deterministic). async=true: returns {handle_id,pgid} immediately, output goes where your command redirects it; needs a self-contained command (no shell_id). shell_id: run inside an existing session (reuse cd/venv). machine: target a declared machine. Stdin-needing programs (sudo): pipe first — echo 'PW' | sudo -S cmd; multi-command: echo 'PW' | sudo -S -v && sudo cmd. Args: cmd (required), shell_id, machine, timeout_seconds, async.")
	add("shell_new", "Open an additional shell session on a machine/env (reuse cd/venv state). Args: machine (optional), purpose (optional label).")
	add("shell_remove", "Terminate and remove a shell session (any state). Args: shell_id (required).")
	add("shell_list", "List shell sessions (id, machine/env, purpose, state). No args.")

	// —— file family (foraging type: never switches the default) ——
	add("file_read", "Read a text file with line numbers and pagination. Args: path (required), machine (optional), offset (1-indexed), limit.")
	add("file_write", "Write a file (atomic temp+rename; content normalized to UTF-8 LF no BOM; advisory warning on sensitive paths like /etc/shadow or .env). Args: path (required), content (required), machine (optional).")
	add("file_patch", "Find-and-replace in a file (exact match; replace_all optional). Args: path, old_string, new_string, replace_all, machine.")
	add("file_search", "Search file contents (substring/regex) or list files by glob under a directory. Args: pattern (required), path (dir), search_type content|files, machine.")

	// —— sense family (read-only observation) ——
	add("process_wait", "Resolve an async handle: {handle_id, state, exit_reason}. state=running while alive; terminal states are exited(N)/signaled(N)/exit_unknown (never a fabricated 0). Args: handle_id (required).")
	add("status", "Full-body read (always true, platform-computed): all envs with lifecycle/backend, process handles alive/dead, capabilities, machines with connection state and mode, default env. Call this first after any wake. No args.")
	add("events", "Replay body events since after_seq (process death, state transitions, disconnections, self-healing). Args: after_seq (int; 0 = from start).")
	add("resource_inspect", "Deep inspection of one resource (default: current env). Args: id (optional).")

	// —— memory family ——
	add("state_get", "Read your memory document (single doc, no keys). First read returns a blank template: record task context, expected processes, next steps; verify live facts with status; never store secrets. No args.")
	add("state_put", "Write your memory document (replaces the whole doc — keep it complete). Args: doc (object, any shape you decide).")

	sort.Strings(s.names)
}

// ToolNames lists the registered tool names (face audit).
func (s *Server) ToolNames() []string { return s.names }

// Run serves the MCP stdio transport (production entry).
func (s *Server) Run(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// CallTool invokes a tool by name with raw JSON args (test/driver entry —
// bypasses the transport, routes through the same handlers the MCP face uses).
func (s *Server) CallTool(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	out, err := s.route(ctx, name, b)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func resText(raw json.RawMessage) string { return string(raw) }

// piggyback attaches body events to any tool response (v3.17 — the response
// is the second 100%-reachable surface). Cursor semantics: each event is
// delivered exactly once per client session (P0 single-client = one cursor
// on the Server); multi-client fan-out lands with events.after_seq exposure.
func (s *Server) piggyback(ctx context.Context) []map[string]any {
	if s.deps.Audit == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, err := s.deps.Audit.Query(audit.Query{Tenant: s.deps.Tenant, AfterSeq: s.evSeq, Limit: 20})
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		out = append(out, map[string]any{"action": e.Action, "resource_id": string(e.ResourceID), "outcome": e.Outcome})
	}
	if n := len(evs); n > 0 {
		s.evSeq = evs[n-1].Seq
	}
	return out
}

// env helpers ----------------------------------------------------------

func argStr(args json.RawMessage, key string) string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func argBool(args json.RawMessage, key string) bool {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return false
	}
	v, _ := m[key].(bool)
	return v
}

func argInt(args json.RawMessage, key string) int {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return 0
	}
	if f, ok := m[key].(float64); ok {
		return int(f)
	}
	return 0
}

// face audit helper — banned substring check lives in the test; this keeps
// the inventory honest in one place.
var _ = resource.StatusRunning
