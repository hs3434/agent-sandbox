package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/container"
	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// execFace / fileFace / buildFace are the data-plane contracts the tool face
// drives (contract-only inter-layer traffic — I7). container.Backend and
// remote.Backend both satisfy the relevant subsets; P0 docker is the default
// route, ssh machines route by name (AD-026 machine selector semantics).
type execFace interface {
	Exec(ctx context.Context, id resource.ResourceID, cmd string, opts ...container.ExecOptions) (container.ExecResult, error)
	ExecAsync(ctx context.Context, id resource.ResourceID, cmd string) (*container.Handle, error)
	Kill(ctx context.Context, id resource.ResourceID, pid int) error
}

type fileFace interface {
	ReadFile(ctx context.Context, id resource.ResourceID, path string) (string, error)
	WriteFile(ctx context.Context, id resource.ResourceID, path, content string) error
}

type buildFace interface {
	Build(ctx context.Context, id resource.ResourceID, opts container.BuildOptions) error
}

// ShellExec runs a command on the default env / named machine. Blocking mode
// returns the full result; async returns a handle (docker only in P0 — ssh
// reports unsupported honestly, AD-026).
func (o *Orchestrator) ShellExec(ctx context.Context, machine, shellID string, async bool, timeoutSecs int, cmd string) (map[string]any, error) {
	if cmd == "" {
		return nil, fmt.Errorf("cmd is required")
	}
	target := machine
	if target == "" {
		target = o.defaultEnv() // follow-style default: last dwelling op
	}
	if target == "" {
		return nil, fmt.Errorf("no env exists yet — container_create first")
	}

	be := o.backendFor(target)
	ef, ok := be.(execFace)
	if !ok {
		return nil, fmt.Errorf("machine %q does not support exec", target)
	}
	id := o.idFor(target)
	if async {
		h, err := ef.ExecAsync(ctx, id, cmd)
		if err != nil {
			return nil, err
		}
		o.auditEvent(id, "tool_call", "shell_exec async", cmd)
		return map[string]any{
			"handle_id": h.ID, "pgid": h.PGID, "state": h.State,
			"note": "output goes where your command redirects it; death arrives via piggyback/status",
		}, nil
	}
	opts := container.ExecOptions{Timeout: time.Duration(timeoutSecs) * time.Second}
	if timeoutSecs <= 0 {
		opts.Timeout = o.execTimeout
	}
	res, err := ef.Exec(ctx, id, cmd, opts)
	if err != nil {
		return nil, err
	}
	o.auditEvent(id, "tool_call", "shell_exec", fmt.Sprintf("exit=%d %s", res.ExitCode, firstLine(cmd)))
	out := map[string]any{
		"exit_code": res.ExitCode, "stdout": res.Stdout, "stderr": res.Stderr,
		"duration_ms": res.Duration.Milliseconds(),
	}
	if res.ExitReason != "" {
		out["exit_reason"] = res.ExitReason
		out["hint"] = "expired: rerun with async=true for long tasks"
	}
	o.switchDefault(target) // dwelling op: follow the workbench
	return out, nil
}

// FileRead/Write/Patch/Search route to the target's fileFace (foraging ops —
// no default switching).
func (o *Orchestrator) FileRead(ctx context.Context, machine, path string) (string, error) {
	be, id, err := o.routeFile(machine)
	if err != nil {
		return "", err
	}
	ff, ok := be.(fileFace)
	if !ok {
		return "", fmt.Errorf("machine %q does not support files", machine)
	}
	return ff.ReadFile(ctx, id, path)
}

func (o *Orchestrator) FileWrite(ctx context.Context, machine, path, content string) error {
	be, id, err := o.routeFile(machine)
	if err != nil {
		return err
	}
	ff, ok := be.(fileFace)
	if !ok {
		return fmt.Errorf("machine %q does not support files", machine)
	}
	return ff.WriteFile(ctx, id, path, content)
}

func (o *Orchestrator) FilePatch(ctx context.Context, machine, path, oldS, newS string, all bool) (int, error) {
	text, err := o.FileRead(ctx, machine, path)
	if err != nil {
		return 0, err
	}
	n := strings.Count(text, oldS)
	if n == 0 {
		return 0, fmt.Errorf("old_string not found in %q", path)
	}
	if !all && n > 1 {
		return 0, fmt.Errorf("old_string matches %d times — pass replace_all or narrow it", n)
	}
	updated := strings.Replace(text, oldS, newS, -1)
	if err := o.FileWrite(ctx, machine, path, updated); err != nil {
		return 0, err
	}
	return n, nil
}

func (o *Orchestrator) FileSearch(ctx context.Context, machine, dir, pattern, searchType string) ([]string, error) {
	// P0 honest implementation: walk + match via FileRead on small sets.
	// ripgrep-scale search lands with real demand (AD-004).
	be, _, err := o.routeFile(machine)
	if err != nil {
		return nil, err
	}
	_ = be
	ff, ok := o.backendFor(o.machineOrDefault(machine)).(fileFace)
	if !ok {
		return nil, fmt.Errorf("machine %q does not support files", machine)
	}
	root := dir
	if root == "" {
		root = "."
	}
	var hits []string
	// single-file search
	if strings.Contains(pattern, "*") && searchType == "files" {
		// glob via ListDir helper is not in the file contract — P0 walks
		// the workdir host-side (docker) or via shell (ssh)
		return o.searchGlob(ctx, o.machineOrDefault(machine), root, pattern)
	}
	// content search: read + match (bounded workdir)
	list, err := o.listFiles(ctx, o.machineOrDefault(machine), root)
	if err != nil {
		return nil, err
	}
	for _, f := range list {
		data, err := ff.ReadFile(ctx, o.idFor(o.machineOrDefault(machine)), f)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(data, "\n") {
			if strings.Contains(line, pattern) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", f, i+1, strings.TrimSpace(line)))
			}
		}
	}
	return hits, nil
}

// Build delegates to the docker backend (container family — ssh reports
// unsupported, AD-027).
func (o *Orchestrator) Build(ctx context.Context, envID, tag, dockerfile string) error {
	bf, ok := o.backendFor(envID).(buildFace)
	if !ok {
		return fmt.Errorf("machine %q does not support builds (thin mode: use companion, P1)", envID)
	}
	return bf.Build(ctx, resource.ResourceID(envID), container.BuildOptions{Tag: tag, Dockerfile: dockerfile})
}

// ListImages returns locally available images (create/build selection basis).
func (o *Orchestrator) ListImages(ctx context.Context) ([]string, error) {
	lm, ok := o.backend.(interface {
		ListImages(ctx context.Context) ([]string, error)
	})
	if !ok {
		return nil, fmt.Errorf("image listing not available on this backend")
	}
	imgs, err := lm.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(imgs)
	return imgs, nil
}

// Status is the full-body read (§4.3): envs + handles + machines + default.
func (o *Orchestrator) Status(ctx context.Context) (map[string]any, error) {
	envs := []map[string]any{}
	records, err := o.store.ListResources(resource.TypeExecEnv)
	if err != nil {
		return nil, err
	}
	for _, m := range records {
		envs = append(envs, map[string]any{
			"id": string(m.ID), "status": m.Status, "backend": m.Backend,
		})
	}
	machines := []map[string]any{}
	for name, be := range o.backends {
		st := "connected"
		if h, ok := be.(interface{ Health() string }); ok {
			st = h.Health()
		}
		machines = append(machines, map[string]any{"name": name, "state": st})
	}
	return map[string]any{
		"envs": envs, "machines": machines,
		"default": o.defaultEnv(),
	}, nil
}

// WorkdirFor exposes the invariant (tool responses tell the agent where files live).
func (o *Orchestrator) WorkdirFor(name string) string {
	return filepath.Join(o.workHome, name)
}

// routing ----------------------------------------------------------------

func (o *Orchestrator) backendFor(name string) any {
	if be, ok := o.backends[name]; ok {
		return be
	}
	return o.backend // default route = docker
}

func (o *Orchestrator) idFor(name string) resource.ResourceID {
	return resource.NewResourceID(o.tenant, resource.TypeExecEnv, name)
}

func (o *Orchestrator) machineOrDefault(machine string) string {
	if machine != "" {
		return machine
	}
	if d := o.defaultEnv(); d != "" {
		return d
	}
	return "default"
}

func (o *Orchestrator) routeFile(machine string) (any, resource.ResourceID, error) {
	name := o.machineOrDefault(machine)
	id := o.idFor(name)
	be := o.backendFor(name)
	return be, id, nil
}

func (o *Orchestrator) defaultEnv() string {
	if o.defaultEnvName != "" {
		return o.defaultEnvName
	}
	// derive from registry: first running env
	records, err := o.store.ListResources(resource.TypeExecEnv)
	if err != nil {
		return ""
	}
	for _, m := range records {
		if m.Status == resource.StatusRunning {
			return lastSeg(string(m.ID))
		}
	}
	if len(records) > 0 {
		return lastSeg(string(records[0].ID))
	}
	return ""
}

func (o *Orchestrator) switchDefault(name string) {
	o.defaultEnvName = name
}

func (o *Orchestrator) auditEvent(id resource.ResourceID, outcome, action, detail string) {
	o.emitWithDuration(id, action, detail, 0)
}

// searchGlob / listFiles: P0 host-side walk for the docker backend (workdir
// is host-visible via the invariant — the one deterministic bridge).
func (o *Orchestrator) searchGlob(ctx context.Context, env, root, pattern string) ([]string, error) {
	base := o.WorkdirFor(env)
	matches, err := filepath.Glob(filepath.Join(base, root, pattern))
	if err != nil {
		return nil, err
	}
	for i, m := range matches {
		if rel, err := filepath.Rel(base, m); err == nil {
			matches[i] = rel
		}
	}
	return matches, nil
}

func (o *Orchestrator) listFiles(ctx context.Context, env, root string) ([]string, error) {
	base := o.WorkdirFor(env)
	dir := filepath.Join(base, root)
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil // errors on unreadable entries are skipped, not fatal
		}
		if info.Size() > 1024*1024 { // skip >1MB files in content search
			return nil
		}
		if rel, rerr := filepath.Rel(base, p); rerr == nil {
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
