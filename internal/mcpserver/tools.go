package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hs3434/agent-sandbox/pkg/audit"
	"github.com/hs3434/agent-sandbox/pkg/file"
	"github.com/hs3434/agent-sandbox/pkg/shell"
	"github.com/hs3434/agent-sandbox/pkg/store"
)

// handlers maps tool name → raw-args handler (CallTool routes here; the MCP
// registration wraps the same functions).
func (s *Server) handlersMap() map[string]func(ctx context.Context, args json.RawMessage) (any, error) {
	return map[string]func(context.Context, json.RawMessage) (any, error){
		"container_create": s.toolContainerCreate,
		"container_remove": s.toolContainerRemove,
		"container_build":  s.toolContainerBuild,
		"container_images": s.toolContainerImages,
		"shell_exec":       s.toolShellExec,
		"shell_new":        s.toolShellNew,
		"shell_remove":     s.toolShellRemove,
		"shell_list":       s.toolShellList,
		"process_wait":     s.toolProcessWait,
		"file_read":        s.toolFileRead,
		"file_write":       s.toolFileWrite,
		"file_patch":       s.toolFilePatch,
		"file_search":      s.toolFileSearch,
		"status":           s.toolStatus,
		"events":           s.toolEvents,
		"resource_inspect": s.toolInspect,
		"state_get":        s.toolStateGet,
		"state_put":        s.toolStatePut,
	}
}

// CallTool dispatches by name (transport-independent driver entry).
func (s *Server) route(ctx context.Context, name string, args json.RawMessage) (any, error) {
	h := s.handlersMap()[name]
	if h == nil {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	return h(ctx, args)
}

// backendUnavailable is the honest error when a tool needs wiring the
// composition root hasn't provided yet (face-audit/test mode).
func (s *Server) backendUnavailable() error {
	return fmt.Errorf("backend not wired (composition root must assemble orchestrator)")
}

// ---- container family (docker-exclusive; AD-027) ----

func (s *Server) toolContainerCreate(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	name := argStr(args, "name")
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	meta, err := s.deps.Orch.Create(ctx, CreateOpts{
		Name:  name,
		Image: argStr(args, "image"),
		Tags:  argTags(args),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": meta.ID, "status": meta.Status, "backend": meta.Backend,
		"workdir": s.deps.Orch.WorkdirFor(name),
		"_events": s.piggyback(ctx),
	}, nil
}

func (s *Server) toolContainerRemove(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	id := s.resolveID(argStr(args, "id"))
	if id == "" {
		id = argStr(args, "name")
	}
	if id == "" {
		return nil, fmt.Errorf("no env exists — container_create first, or pass name/id")
	}
	if err := s.deps.Orch.Remove(ctx, id, argBool(args, "purge")); err != nil {
		return nil, err
	}
	return map[string]any{"removed": id, "workdir_kept": !argBool(args, "purge"),
		"_events": s.piggyback(ctx)}, nil
}

func (s *Server) toolContainerBuild(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	tag := argStr(args, "tag")
	if tag == "" {
		return nil, fmt.Errorf("tag is required (explicit tags prevent collision)")
	}
	env := s.resolveID(argStr(args, "id"))
	if env == "" {
		env = argStr(args, "name")
	}
	if env == "" {
		return nil, fmt.Errorf("no id given and no default env set")
	}
	if err := s.deps.Orch.Build(ctx, env, tag, argStr(args, "dockerfile")); err != nil {
		return nil, err
	}
	return map[string]any{"tag": tag, "env": env, "next": "remove + same-name create with this image to swap the body",
		"_events": s.piggyback(ctx)}, nil
}

func (s *Server) toolContainerImages(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	imgs, err := s.deps.Orch.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"images": imgs}, nil
}

// ---- shell family ----

func (s *Server) toolShellExec(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	cmd := argStr(args, "cmd")
	if cmd == "" {
		return nil, fmt.Errorf("cmd is required")
	}
	execOpts := ExecOpts{
		Async:   argBool(args, "async"),
		Machine: argStr(args, "machine"),
		ShellID: argStr(args, "shell_id"),
		Timeout: argInt(args, "timeout_seconds"),
	}
	res, err := s.deps.Orch.ShellExec(ctx, execOpts, cmd)
	if err != nil {
		return nil, err
	}
	res["_events"] = s.piggyback(ctx)
	return res, nil
}

func (s *Server) toolShellNew(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	sh, err := s.deps.Orch.ShellNew(ctx, argStr(args, "machine"), argStr(args, "purpose"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"shell": sh, "_events": s.piggyback(ctx)}, nil
}

func (s *Server) toolShellRemove(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	id := argStr(args, "shell_id")
	if id == "" {
		return nil, fmt.Errorf("shell_id is required")
	}
	return map[string]any{"removed": s.deps.Orch.ShellRemove(id), "_events": s.piggyback(ctx)}, nil
}

func (s *Server) toolShellList(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	shells := s.deps.Orch.ShellList()
	return map[string]any{"shells": shells, "count": len(shells)}, nil
}

// ---- file family (foraging) ----

// toolProcessWait resolves an async handle (both axes): ledger row truth —
// running → state field; dead → terminal state (exited(N)/signaled(N)/
// exit_unknown). The honest-wait surface for async exec.
func (s *Server) toolProcessWait(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	handleID := argStr(args, "handle_id")
	if handleID == "" {
		return nil, fmt.Errorf("handle_id is required")
	}
	row, err := s.deps.Orch.HandleState(handleID)
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Server) toolFileRead(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	path := argStr(args, "path")
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	text, err := s.deps.Orch.FileRead(ctx, argStr(args, "machine"), path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "content": text, "durable": s.deps.Orch.PathDurable(argStr(args, "machine"), path)}, nil
}

func (s *Server) toolFileWrite(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	path := argStr(args, "path")
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	content := argStr(args, "content")
	res := map[string]any{"path": path, "written": true, "durable": s.deps.Orch.PathDurable(argStr(args, "machine"), path)}
	if w := file.SensitivePath(path); w != "" {
		res["warning"] = w // advisory, never blocking (safety.py semantics)
	}
	if err := s.deps.Orch.FileWrite(ctx, argStr(args, "machine"), path, content); err != nil {
		return nil, err
	}
	res["_events"] = s.piggyback(ctx)
	return res, nil
}

func (s *Server) toolFilePatch(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	oldStr := argStr(args, "old_string")
	if oldStr == "" {
		return nil, fmt.Errorf("old_string is required")
	}
	n, err := s.deps.Orch.FilePatch(ctx, argStr(args, "machine"), argStr(args, "path"),
		oldStr, argStr(args, "new_string"), argBool(args, "replace_all"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"replacements": n, "_events": s.piggyback(ctx)}, nil
}

func (s *Server) toolFileSearch(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	pattern := argStr(args, "pattern")
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	res, err := s.deps.Orch.FileSearch(ctx, shell.SearchParams{
		Pattern:     pattern,
		Path:        argStr(args, "path"),
		SearchType:  argStr(args, "search_type"),
		FileGlob:    argStr(args, "file_glob"),
		Limit:       argInt(args, "limit"),
		Offset:      argInt(args, "offset"),
		OutputMode:  argStr(args, "output_mode"),
		Context:     argInt(args, "context"),
		TimeoutSecs: argInt(args, "timeout_seconds"),
	}, argStr(args, "machine"))
	if err != nil {
		return nil, err
	}
	out := map[string]any{"matches": res.Matches, "type": res.Type, "engine": res.Engine}
	if res.Diagnostics != "" {
		out["diagnostics"] = res.Diagnostics
	}
	if res.Truncated {
		out["truncated"] = true
	}
	return out, nil
}

// ---- sense family ----

func (s *Server) toolStatus(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	st, err := s.deps.Orch.Status(ctx)
	if err != nil {
		return nil, err
	}
	deriveModeStrings(st)
	st["_events"] = s.piggyback(ctx)
	return st, nil
}

// deriveModeStrings renders the machine mode enum into agent-neutral human
// wording (D-29: never teach install — the trust gate belongs to the operator).
func deriveModeStrings(st map[string]any) {
	for axis := range st {
		rows, ok := st[axis].(map[string]any)
		if !ok {
			continue
		}
		for _, rowAny := range rows {
			row, ok := rowAny.(map[string]any)
			if !ok {
				continue
			}
			switch row["mode"] {
			case "companion":
				row["mode_detail"] = "sandboxd-server protocol peer active"
			case "degraded":
				row["mode_detail"] = "companion unreachable, falling back"
			case "thin":
				row["mode_detail"] = "managed by operator"
			}
		}
	}
}

func (s *Server) toolEvents(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Audit == nil {
		return nil, s.backendUnavailable()
	}
	evs, err := s.deps.Audit.Query(audit.Query{Tenant: s.deps.Tenant, Limit: 200})
	if err != nil {
		return nil, err
	}
	next := int64(0)
	out := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		out = append(out, map[string]any{
			"seq": next, "ts": e.Timestamp.Unix(), "action": e.Action,
			"resource_id": string(e.ResourceID), "outcome": e.Outcome,
			"detail": e.Detail,
		})
	}
	return map[string]any{"events": out, "cursor": next}, nil
}

func (s *Server) toolInspect(ctx context.Context, args json.RawMessage) (any, error) {
	if s.deps.Orch == nil {
		return nil, s.backendUnavailable()
	}
	id := s.resolveID(argStr(args, "id"))
	if id == "" {
		id = argStr(args, "name")
	}
	if id == "" {
		return nil, fmt.Errorf("no env exists — container_create first, or pass name/id")
	}
	return s.deps.Orch.Inspect(ctx, id)
}

// ---- memory family (single doc, no keys — AD-012) ----

var blankTemplate = map[string]any{
	"_guide": "Your memory: task context, processes that should be running, next steps. Verify live facts with status(). NEVER store secrets here (plaintext storage).",
}

func (s *Server) toolStateGet(ctx context.Context, args json.RawMessage) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deps.Store == nil {
		// test mode: in-memory doc
		if s.stateDoc == nil {
			return map[string]any{"doc": blankTemplate}, nil
		}
		var doc map[string]any
		json.Unmarshal(s.stateDoc, &doc)
		return map[string]any{"doc": doc}, nil
	}
	m, err := s.deps.Store.GetStateDoc()
	if err == store.ErrStateEmpty {
		return map[string]any{"doc": blankTemplate}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if json.Unmarshal(m, &doc) != nil {
		return nil, fmt.Errorf("stored state doc is not valid JSON")
	}
	return map[string]any{"doc": doc}, nil
}

func (s *Server) toolStatePut(ctx context.Context, args json.RawMessage) (any, error) {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return nil, fmt.Errorf("doc object is required")
	}
	doc, ok := m["doc"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("doc (object) is required — the single memory doc replaces wholly")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if len(raw) > StateDocMax {
		return nil, fmt.Errorf("doc exceeds %d bytes — memory is for pointers and plans, not bulk data", StateDocMax)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deps.Store != nil {
		if err := s.deps.Store.PutStateDoc(raw); err != nil {
			return nil, err
		}
	}
	s.stateDoc = raw
	return map[string]any{"saved": true, "bytes": len(raw)}, nil
}

// helpers

func argTags(args json.RawMessage) map[string]string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return nil
	}
	tags, _ := m["tags"].(map[string]any)
	out := map[string]string{}
	for k, v := range tags {
		if sv, ok := v.(string); ok {
			out[k] = sv
		}
	}
	return out
}

func (s *Server) resolveID(id string) string {
	if id != "" {
		return id
	}
	if s.deps.DefaultEnv != "" {
		return s.deps.DefaultEnv
	}
	// derive from status (the one confirmation source — spec §4.3)
	if s.deps.Orch != nil {
		if st, err := s.deps.Orch.Status(context.Background()); err == nil {
			if d, ok := st["default"].(string); ok && d != "" {
				return d
			}
		}
	}
	return ""
}
