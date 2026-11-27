package orchestrator

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/container"
	"github.com/hs3434/agent-sandbox/pkg/resource"
	"github.com/hs3434/agent-sandbox/pkg/shell"
	"github.com/hs3434/agent-sandbox/pkg/store"
)

// execFace / fileFace / buildFace are the data-plane contracts the tool face
// drives (contract-only inter-layer traffic — I7). container.Backend and
// remote.Backend both satisfy the relevant subsets; P0 docker is the default
// route, ssh machines route by name (AD-026 machine selector semantics).
type execFace interface {
	Exec(ctx context.Context, id resource.ResourceID, cmd string, opts ...container.ExecOptions) (container.ExecResult, error)
}

// asyncExecFace is the subset that can detach a process (docker only in P0:
// the ssh thin mode is blocking-only by design, AD-026).
type asyncExecFace interface {
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

// networkFace reports a body's network address (where others can reach it).
type networkFace interface {
	NetworkInfo(ctx context.Context, id resource.ResourceID) (container.NetAddress, error)
}

// ShellExec runs a command on the default env / named machine. Blocking mode
// returns the full result; async returns a handle (docker only in P0 — ssh
// reports unsupported honestly, AD-026).
func (o *Orchestrator) ShellExec(ctx context.Context, machine, shellID string, async bool, timeoutSecs int, cmd string) (map[string]any, error) {
	if cmd == "" {
		return nil, fmt.Errorf("cmd is required")
	}
	// session mode: run inside a persistent shell (state persists across calls)
	if shellID != "" {
		if async {
			return nil, fmt.Errorf("session exec is blocking-only (the session itself is the persistence)")
		}
		return o.ShellExecInSession(ctx, shellID, cmd, timeoutSecs)
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
		af, ok := be.(asyncExecFace)
		if !ok {
			return nil, fmt.Errorf("machine %q does not support async exec (thin mode is blocking-only; async needs an active companion)", target)
		}
		h, err := af.ExecAsync(ctx, id, cmd)
		if err != nil {
			return nil, err
		}
		// ledger = the platform's record of what we manage (AD-030): a
		// running async process is ours the moment it exists. Write-ahead —
		// if we crash here, the ledger row (created) is the recovery seed.
		exeEnv := "docker"
		if _, isComp := be.(interface {
			ReconcileLedger(ctx context.Context) ([]map[string]any, error)
		}); isComp {
			exeEnv = "companion" // boot reconcile routes this row to daemon truth (D-21)
		}
		if err := o.store.PutHandle(store.Handle{
			ID: h.ID, EnvID: h.EnvID, PGID: h.PGID, Cmd: h.Cmd,
			State:     store.HandleRunning,
			ExeEnv:    exeEnv,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return nil, fmt.Errorf("ledger write (write-ahead): %w", err)
		}
		ef, isC := be.(interface {
			SetHandleDeadHook(func(handleID string, terminal string))
		})
		if isC {
			ef.SetHandleDeadHook(o.markHandleDead)
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

// FileSearch runs ripgrep ON THE TARGET (ported from the Python predecessor):
// a host-side walk only ever saw the docker workdir and silently missed every
// remote machine. The target's own rg gives real regex/glob/context semantics
// on both axes.
func (o *Orchestrator) FileSearch(ctx context.Context, p shell.SearchParams, machine string) (*shell.SearchResult, error) {
	if p.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	name := o.machineOrDefault(machine)
	be := o.backendFor(name)
	ef, ok := be.(execFace)
	if !ok {
		return nil, fmt.Errorf("machine %q does not support exec", name)
	}
	id := o.idFor(name)
	d := o.dialectFor(name)
	searchTimeout := 60 * time.Second
	if p.TimeoutSecs > 0 {
		searchTimeout = time.Duration(min(p.TimeoutSecs, 300)) * time.Second // D-27 ceiling
	}
	out, err := shell.Search(ctx, func(ctx context.Context, cmd string) (string, int, error) {
		r, err := ef.Exec(ctx, id, cmd, container.ExecOptions{Timeout: searchTimeout})
		if err != nil {
			return "", 0, err
		}
		return r.Stdout + r.Stderr, r.ExitCode, nil
	}, d, p)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// dialectFor picks the shell dialect of the target's OS.
func (o *Orchestrator) dialectFor(name string) shell.Dialect {
	if be, ok := o.backends[name]; ok {
		if t, ok := be.(interface{ TargetInfo() (string, string) }); ok {
			_, osType := t.TargetInfo()
			return shell.DialectFor(osType)
		}
	}
	return shell.PosixDialect{}
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

// Status is the full-body read (§4.3): what the registry knows about every
// body we manage, plus the machines we can reach and the current default.
//
// Shape discipline (v3.26): collections are name-keyed maps, the pointer is
// a scalar — `default` names one entry of `envs`, it is not a peer list.
// Everything here is read from the database + live connection facts; nothing
// is guessed.
func (o *Orchestrator) Status(ctx context.Context) (map[string]any, error) {
	envs := map[string]any{}
	records, err := o.store.ListResources(resource.TypeExecEnv)
	if err != nil {
		return nil, err
	}
	for _, m := range records {
		name := lastSeg(string(m.ID))
		// shallow view (§4.3: status = 全景浅查, inspect = 单资源深查):
		// only what an agent needs to act right now. Identity (id/tenant),
		// backend label and creation time live in resource_inspect.
		row := map[string]any{
			"status":  string(m.Status),
			"image":   m.Image,
			"workdir": o.WorkdirFor(name),
		}
		if len(m.Tags) > 0 {
			row["tags"] = m.Tags
		}
		// network address: where other bodies can reach this one (§13.2)
		if nf, ok := o.backendFor(name).(networkFace); ok {
			if addr, err := nf.NetworkInfo(ctx, m.ID); err == nil {
				row["network"] = map[string]any{
					"name": addr.Network, "ip": addr.IP, "dns": addr.DNS,
				}
			}
		}
		envs[name] = row
	}

	// machines are grouped by axis: the payload names the deployed axes
	// (docker / ssh / ...), not an abstract category.
	axes := map[string]map[string]any{}
	for name, be := range o.backends {
		row := map[string]any{}
		if h, ok := be.(interface{ Health() string }); ok {
			row["state"] = h.Health()
		}
		// machine mode enum (T2, D-01/D-29). Marshal to string HERE — the
		// renderer compares string constants and MachineMode is a distinct
		// type (interface inequality would silently skip every case).
		row["mode"] = string(o.machineMode(name))
		axis := "ssh"
		if k, ok := be.(interface{ Axis() string }); ok {
			axis = k.Axis()
		}
		if t, ok := be.(interface{ TargetInfo() (string, string) }); ok {
			addr, osType := t.TargetInfo()
			row["address"] = addr
			row["os"] = osType
		}
		if axes[axis] == nil {
			axes[axis] = map[string]any{}
		}
		axes[axis][name] = row
	}

	// Payload names the deployed axes, not abstract categories (§13.2
	// matrix): "docker" = the docker bodies this instance runs, "ssh" = the
	// ssh targets it can reach. A future axis (k8s/vm) adds its own key
	// instead of overloading these.
	out := map[string]any{
		"docker":  envs,           // collection: body name → body facts
		"default": o.defaultEnv(), // pointer: the name of one body (the workbench)
	}
	// axis collections: "ssh" always present in P0 (stable schema), a future
	// axis (k8s/vm) adds its own key when it exists.
	for axis, rows := range axes {
		out[axis] = rows
	}
	if _, ok := out["ssh"]; !ok {
		out["ssh"] = map[string]any{}
	}
	return out, nil
}

// HandleState resolves one handle (process_wait surface). Ledger-only is a
// lie while a companion lives: its reaper may have reaped the death after
// our last event pull, so running rows are pulled from daemon truth FIRST
// (D-21: the ledger must not assert a live process's death — nor a dead
// process's life).
func (o *Orchestrator) HandleState(handleID string) (map[string]any, error) {
	h, err := o.store.GetHandle(handleID)
	if err != nil {
		return nil, fmt.Errorf("handle %q: %w", handleID, err)
	}
	if h.State == "running" && h.ExeEnv == "companion" {
		if mc, ok := o.backends[lastSeg(string(h.EnvID))]; ok {
			if wh, ok := mc.(interface {
				WaitHandle(ctx context.Context, handleID string) (*container.Handle, error)
			}); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				got, err := wh.WaitHandle(ctx, h.ID)
				cancel()
				if err != nil {
					// first pull after an idle gap often wakes a dropped
					// bridge (transport err → drop): ONE retry on the fresh
					// dial before trusting the ledger's stale row
					ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
					got, err = wh.WaitHandle(ctx2, h.ID)
					cancel2()
				}
				if err == nil && got != nil {
					if got.State == container.HandleRunning {
						h.State = "running"
					} else {
						h.State = "dead"
						h.ExitReason = fmt.Sprintf("exited(%d)", got.ExitCode)
					}
					_ = o.store.PutHandle(*h) // pull-truth lands in the ledger too
				}
				// persistent pull error: ledger stands (honest last-known, D-21)
			}
		}
	}
	out := map[string]any{
		"handle_id":   h.ID,
		"state":       h.State,
		"exit_reason": h.ExitReason,
		"cmd":         h.Cmd,
	}
	return out, nil
}

// MachineMode is the T2 enum: thin = ssh passthrough (managed by operator),
// companion = sandboxd-server protocol peer, degraded = companion expected
// but unreachable. Rendering derives human strings from the enum value —
// the flip logic (W2+) only ever changes the enum, never prose.
type MachineMode string

const (
	ModeThin      MachineMode = "thin"
	ModeCompanion MachineMode = "companion"
	ModeDegraded  MachineMode = "degraded"
)

// machineMode resolves the current mode enum for a machine (T2).
func (o *Orchestrator) machineMode(name string) MachineMode {
	if mc, ok := o.backends[name]; ok {
		if c, ok := mc.(interface{ CompanionState() string }); ok && c.CompanionState() == "active" {
			return ModeCompanion
		}
	}
	return ModeThin
}

// Inspect is the deep single-resource view (§4.3): identity, provenance,
// timing — everything status deliberately leaves out.
func (o *Orchestrator) Inspect(ctx context.Context, id string) (map[string]any, error) {
	rid, err := o.ResolveID(id)
	if err != nil {
		return nil, err
	}
	m, err := o.store.GetResource(rid)
	if err != nil {
		return nil, err
	}
	name := lastSeg(string(m.ID))
	out := map[string]any{
		"id":         string(m.ID),
		"name":       name,
		"type":       string(m.Type),
		"backend":    m.Backend,
		"status":     string(m.Status),
		"image":      m.Image,
		"workdir":    o.WorkdirFor(name),
		"created_at": m.CreatedAt.UTC().Format(time.RFC3339),
	}
	if len(m.Tags) > 0 {
		out["tags"] = m.Tags
	}
	if m.Owner != "" {
		out["owner"] = m.Owner
	}
	if nf, ok := o.backendFor(name).(networkFace); ok {
		if addr, err := nf.NetworkInfo(ctx, rid); err == nil {
			out["network"] = map[string]any{
				"name": addr.Network, "ip": addr.IP, "dns": addr.DNS,
			}
		}
	}
	return out, nil
}

// WorkdirFor exposes the invariant (tool responses tell the agent where files live).
func (o *Orchestrator) WorkdirFor(name string) string {
	return filepath.Join(o.workHome, name)
}

// PathDurable is the D-11 honesty flag: docker axis — durable iff the path
// lives in the bind-mount (workspace; the invariant f(work_home, env_name)
// zone that survives rebuild); ssh axis — the whole machine is durable
// (machine-is-env). Never blocks: pure disclosure.
func (o *Orchestrator) PathDurable(machine, path string) bool {
	name := o.machineOrDefault(machine)
	if _, isRemote := o.backends[name]; isRemote {
		return true
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/workspace") {
		return false
	}
	// resolve .. segments: /workspace/../escape must NOT count (D-28 case)
	resolved := filepath.Clean(p)
	return resolved == "/workspace" || strings.HasPrefix(resolved, "/workspace/")
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

// markHandleDead is the docker backend's death-notification consumer: ledger
// row flips to dead (explicit-only transition) + audit event so the death
// reaches agents via piggyback/status.
func (o *Orchestrator) markHandleDead(handleID string, terminal string) {
	if terminal == "" {
		terminal = "exit_unknown" // the one coercion allowed: absent → unknown, never 0 (D-21)
	}
	if err := o.store.MarkHandleDead(handleID, terminal); err != nil {
		// unknown handle = pre-ledger leftover; the fact is still auditable
		o.auditEvent("", "process_event", "handle dead (unrecorded)", handleID+" "+terminal)
		return
	}
	o.auditEvent("", "process_event", "handle dead", handleID+" "+terminal)
}

// searchGlob / listFiles: P0 host-side walk for the docker backend (workdir
// is host-visible via the invariant — the one deterministic bridge).

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---- persistent shell sessions (marker framing, no PTY) ----

// sessionRecord is one live shell session in the registry.
type sessionRecord struct {
	s       *shell.Session
	purpose string
	created time.Time
}

// ShellNew opens a persistent shell on the target (default env when no
// machine is given) and registers it.
func (o *Orchestrator) ShellNew(ctx context.Context, machine, purpose string) (*SessionInfo, error) {
	// Sessions must outlive the tool call that created them: the transport
	// runs on a process-lifetime context, not the request ctx (whose
	// cancellation would kill the shell the moment shell_new returns).
	_ = ctx
	name := o.machineOrDefault(machine)
	be := o.backendFor(name)
	d := o.dialectFor(name)

	var stdin io.WriteCloser
	var stdout io.Reader
	var teardown func()
	var err error
	switch tb := be.(type) {
	case interface {
		OpenSession(context.Context, resource.ResourceID, []string) (io.WriteCloser, io.Reader, func(), error)
	}:
		stdin, stdout, teardown, err = tb.OpenSession(o.sessCtx, o.idFor(name), d.ShellArgs())
	case interface {
		OpenSession(context.Context, string, string) (io.WriteCloser, io.Reader, func(), error)
	}:
		stdin, stdout, teardown, err = tb.OpenSession(o.sessCtx, name, strings.Join(d.ShellArgs(), " "))
	default:
		return nil, fmt.Errorf("machine %q does not support sessions", name)
	}
	if err != nil {
		return nil, err
	}

	id := fmt.Sprintf("sh-%d", time.Now().UnixNano())
	s := shell.NewSession(id, name, d, func(b []byte) error { _, werr := stdin.Write(b); return werr }, stdout, teardown)
	o.mu.Lock()
	if o.sessions == nil {
		o.sessions = map[string]*sessionRecord{}
	}
	o.sessions[id] = &sessionRecord{s: s, purpose: purpose, created: time.Now()}
	o.mu.Unlock()
	return &SessionInfo{ID: id, Env: name, Dialect: d.Name(), Purpose: purpose, State: "ready"}, nil
}

// SessionInfo is the shell_new / shell_list payload row.
type SessionInfo struct {
	ID      string `json:"id"`
	Env     string `json:"env"`
	Dialect string `json:"dialect"`
	Purpose string `json:"purpose,omitempty"`
	State   string `json:"state"`
	Created string `json:"created"`
}

// ShellExecInSession runs one blocking command inside a registered session.
func (o *Orchestrator) ShellExecInSession(ctx context.Context, shellID, cmd string, timeoutSecs int) (map[string]any, error) {
	o.mu.Lock()
	rec := o.sessions[shellID]
	o.mu.Unlock()
	if rec == nil {
		return nil, fmt.Errorf("shell %q not found (shell_list for the inventory)", shellID)
	}
	out, code, err := rec.s.Send(cmd, time.Duration(timeoutSecs)*time.Second)
	res := map[string]any{
		"shell_id":  shellID,
		"exit_code": code,
		"stdout":    out,
	}
	if err != nil {
		res["note"] = err.Error()
	}
	return res, nil
}

// ShellRemove kills and unregisters a session.
func (o *Orchestrator) ShellRemove(shellID string) bool {
	o.mu.Lock()
	rec := o.sessions[shellID]
	if rec != nil {
		delete(o.sessions, shellID)
	}
	o.mu.Unlock()
	if rec == nil {
		return false
	}
	rec.s.Close()
	return true
}

// ShellList reports the live sessions (registry truth).
func (o *Orchestrator) ShellList() []SessionInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]SessionInfo, 0, len(o.sessions))
	for id, rec := range o.sessions {
		state := rec.s.Health()
		if rec.s.Busy() {
			state = "busy"
		}
		out = append(out, SessionInfo{
			ID: id, Env: rec.s.Env, Dialect: rec.s.Dialect.Name(),
			Purpose: rec.purpose, State: state,
			Created: rec.created.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
