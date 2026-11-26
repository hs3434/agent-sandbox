// Package container implements the docker backend: the P0 execution body
// (design-spec-v3 §10, AD-027 container family).
//
// Contract surface: Backend struct with Create/Remove/Exec/ExecAsync/Kill/
// WaitHandle/ReadFile/WriteFile/Build/Capabilities. Same-name recreate
// (self-healing) is orchestration-layer logic; this package provides the
// primitives. workdir invariant lives here: work_home/<name> is the only
// bind-mount, giving the daemon a stable host path (spec §7.0).
package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"encoding/base64"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/hs3434/agent-sandbox/pkg/file"
	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// Declared defaults (I6 declaration surface; sandbox.yaml overrides land in m4).
const (
	DefaultExecTimeout = 60 * time.Second
	DefaultStopTimeout = 10 // seconds, graceful stop window (backstop; PID 1 traps SIGTERM → ~0.5s)
	WorkdirInContainer = "/workspace"
)

// Handle states for async exec (spec §4.2).
const (
	HandleRunning = "running"
	HandleDead    = "dead"
)

// Handle is one async process record (pgid == container exec PID inside the
// container namespace; process-group kill runs pkill -g inside the container).
type Handle struct {
	ID       string
	EnvID    resource.ResourceID
	ExecID   string // docker exec instance id
	PGID     int    // pid inside container namespace
	Cmd      string
	State    string
	ExitCode int
}

// Options configures the docker backend.
type Options struct {
	WorkHome string // host path; workdir = WorkHome/<name> (invariant)
	Network  string // optional user-defined bridge (DNS network, §13.2)
	Tenant   string // resource identity tenant (default: "local")
}

// Backend is the docker data-plane implementation.
type Backend struct {
	cli          *client.Client
	workHome     string
	tenant       string
	network      string
	onHandleDead func(handleID string, terminal string)
}

// Open connects to the local docker daemon.
func Open(opts Options) (*Backend, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		cli.Close()
		return nil, fmt.Errorf("docker daemon unreachable: %w", err)
	}
	if opts.WorkHome == "" {
		cli.Close()
		return nil, fmt.Errorf("work_home is required (workdir invariant)")
	}
	if err := os.MkdirAll(opts.WorkHome, 0o755); err != nil {
		cli.Close()
		return nil, fmt.Errorf("work_home mkdir: %w", err)
	}
	tenant := opts.Tenant
	if tenant == "" {
		tenant = "local"
	}
	return &Backend{cli: cli, workHome: opts.WorkHome, network: opts.Network, tenant: tenant}, nil
}

func (b *Backend) Close() error { return b.cli.Close() }

// Client exposes the docker client (tests and probes only).
func (b *Backend) Client() *client.Client { return b.cli }

// Capabilities declares what this backend guarantees (platform primitives only).
func (b *Backend) Capabilities() []resource.Capability {
	return []resource.Capability{resource.CapShell, resource.CapFiles}
}

func workdirFor(workHome, name string) string {
	return filepath.Join(workHome, name)
}

// hostPath converts an agent-visible container path to the daemon-side host
// path. Only valid inside the bind-mount (workdir) — the one deterministic
// bridge between container and host worlds (spec §7.0).
func (b *Backend) hostPath(envName, containerPath string) (string, error) {
	rel := strings.TrimPrefix(filepath.ToSlash(containerPath), WorkdirInContainer)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "."
	}
	host := filepath.Join(workdirFor(b.workHome, envName), filepath.FromSlash(rel))
	// containment check: resolved path must stay under the env workdir
	resolved, err := filepath.Abs(host)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", containerPath, err)
	}
	base, err := filepath.Abs(workdirFor(b.workHome, envName))
	if err != nil || resolved != base && !strings.HasPrefix(resolved, base+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workdir (constraint: resolution ⊆ workdir)", containerPath)
	}
	return resolved, nil
}

// Create runs a container from spec with the workdir bind-mount. The image is
// pulled when absent (cold-start path, NFR-9).
func (b *Backend) Create(ctx context.Context, name string, spec resource.ExecEnvSpec) (*resource.Meta, error) {
	img := spec.Image
	if img == "" {
		return nil, fmt.Errorf("image is required")
	}
	if err := b.ensureImage(ctx, img); err != nil {
		return nil, err
	}
	if b.network != "" {
		if err := b.ensureNetwork(ctx, b.network); err != nil {
			return nil, err
		}
	}
	wd := workdirFor(b.workHome, name)
	if err := os.MkdirAll(wd, 0o755); err != nil {
		return nil, fmt.Errorf("workdir mkdir: %w", err)
	}
	cfg := &container.Config{
		Image: img,
		// Keep the container alive without a real workload: a sleeping shell
		// the agent's execs attach around. PID 1 must honour SIGTERM —
		// `sleep infinity` ignores it, so every stop burned the full grace
		// window (60s) before SIGKILL.
		Cmd: []string{"sh", "-c", "trap 'exit 0' TERM; sleep infinity & wait"},
		WorkingDir: WorkdirInContainer,
		Tty:   false,
	}
	hostCfg := &container.HostConfig{
		Binds: []string{wd + ":" + WorkdirInContainer},
	}
	if b.network != "" {
		hostCfg.NetworkMode = container.NetworkMode(b.network)
	}
	body, err := b.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, name)
	if err != nil {
		return nil, fmt.Errorf("container create: %w", err)
	}
	if err := b.cli.ContainerStart(ctx, body.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("container start: %w", err)
	}
	return &resource.Meta{
		ID:        resource.NewResourceID(b.tenant, resource.TypeExecEnv, name),
		Type:      resource.TypeExecEnv,
		Backend:   "docker",
		Status:    resource.StatusRunning,
		CreatedAt: time.Now(),
	}, nil
}

func (b *Backend) ensureImage(ctx context.Context, img string) error {
	if _, _, err := b.cli.ImageInspectWithRaw(ctx, img); err == nil {
		return nil // cache hit (NFR-1/9 fast path)
	}
	rc, err := b.cli.ImagePull(ctx, img, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("image pull %q: %w", img, err)
	}
	defer rc.Close()
	io.Copy(io.Discard, rc) // drain; errors surface at first use
	return nil
}

// NetAddress is a body's address on its backend network (§13.2): the
// user-defined bridge it lives on, its IP there, and the DNS name other
// bodies use to reach it (container name = DNS name on a user-defined
// bridge).
type NetAddress struct {
	Network string // user-defined bridge name ("" = daemon default bridge)
	IP      string
	DNS     string
}

// NetworkInfo reports the body's network address. Honest failure: an
// unreachable container is an error, not an empty address.
func (b *Backend) NetworkInfo(ctx context.Context, id resource.ResourceID) (NetAddress, error) {
	name := b.containerName(id)
	insp, err := b.cli.ContainerInspect(ctx, name)
	if err != nil {
		return NetAddress{}, fmt.Errorf("inspect %q: %w", name, err)
	}
	addr := NetAddress{Network: b.network, DNS: name}
	for _, n := range insp.NetworkSettings.Networks {
		addr.IP = n.IPAddress
		if addr.Network == "" {
			addr.Network = n.NetworkID
		}
		break
	}
	return addr, nil
}

// ListNames exposes daemon container names for reconcile (registry × daemon
// comparison, spec §3.1). Sandbox-managed containers carry the sandboxd label.
func (b *Backend) ListNames(ctx context.Context) ([]string, error) {
	list, err := b.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("container list: %w", err)
	}
	var out []string
	for _, c := range list {
		for _, n := range c.Names {
			out = append(out, strings.TrimPrefix(n, "/"))
		}
	}
	return out, nil
}

// ListImages enumerates locally available images (container_images tool —
// the selection basis for create/build).
func (b *Backend) ListImages(ctx context.Context) ([]string, error) {
	list, err := b.cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("image list: %w", err)
	}
	var out []string
	for _, img := range list {
		out = append(out, img.RepoTags...)
	}
	return out, nil
}

func (b *Backend) ensureNetwork(ctx context.Context, name string) error {
	if _, err := b.cli.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return nil
	}
	_, err := b.cli.NetworkCreate(ctx, name, network.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("network create %q: %w", name, err)
	}
	return nil
}

func (b *Backend) containerName(id resource.ResourceID) string {
	parts := strings.SplitN(string(id), ":", 3)
	return parts[len(parts)-1]
}

// Remove deletes the container. The workdir is kept unless purge — the name
// is the address of an asset, not the instance's attribute (spec §7.0).
func (b *Backend) Remove(ctx context.Context, id resource.ResourceID, purge bool) error {
	name := b.containerName(id)
	timeout := DefaultStopTimeout
	if err := b.cli.ContainerStop(ctx, name, container.StopOptions{Timeout: &timeout}); err != nil {
		// already gone is fine — idempotent removal
		if !isNotFound(err) {
			return fmt.Errorf("container stop: %w", err)
		}
	}
	if err := b.cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); err != nil && !isNotFound(err) {
		return fmt.Errorf("container remove: %w", err)
	}
	if purge {
		return os.RemoveAll(workdirFor(b.workHome, name))
	}
	return nil
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "No such")
}

// ExecOptions tunes a blocking exec.
type ExecOptions struct {
	Timeout time.Duration // default DefaultExecTimeout; expiry kills the process group
}

// ExecResult is the unified blocking-exec result (spec §4.2: output in the
// return value; timeout is a deterministic fact, never a hang).
type ExecResult struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	ExitReason string // "" | "timeout"
	Duration   time.Duration
}

// Exec runs a command to completion (blocking mode).
func (b *Backend) Exec(ctx context.Context, id resource.ResourceID, cmd string, opts ...ExecOptions) (ExecResult, error) {
	cfg := ExecOptions{Timeout: DefaultExecTimeout}
	if len(opts) > 0 {
		if opts[0].Timeout > 0 {
			cfg = opts[0]
		}
	}
	execCfg := container.ExecOptions{
		Cmd:          []string{"/bin/sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
	}
	attach, err := b.cli.ContainerExecCreate(ctx, b.containerName(id), execCfg)
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec create: %w", err)
	}
	start := time.Now()
	resp, err := b.cli.ContainerExecAttach(ctx, attach.ID, container.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	var outBuf, errBuf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(&outBuf, &errBuf, resp.Reader)
		done <- err
	}()

	timer := time.NewTimer(cfg.Timeout)
	defer timer.Stop()
	var timedOut bool
	var execPid int
	// fetch the exec's own pid so expiry can kill exactly this command tree
	if insp, err := b.cli.ContainerExecInspect(ctx, attach.ID); err == nil {
		execPid = insp.Pid
	}
	select {
	case <-done:
	case <-timer.C:
		timedOut = true
		// deterministic expiry (spec §4.2): kill the exec pid and its whole
		// process tree via pkill -T (parent-tree), fallback plain pid kill.
		// docker exec has no process-group semantics; the honest equivalent
		// is "kill this exec's pid + its children".
		killCmd := fmt.Sprintf("pkill -TERM -P %d 2>/dev/null; kill -TERM %d 2>/dev/null; sleep 0.2; pkill -KILL -P %d 2>/dev/null; kill -KILL %d 2>/dev/null; true",
			execPid, execPid, execPid, execPid)
		killCfg := container.ExecOptions{Cmd:          []string{"/bin/sh", "-c", killCmd},
			AttachStdout: false, AttachStderr: false, Detach: true}
		if k, err := b.cli.ContainerExecCreate(ctx, b.containerName(id), killCfg); err == nil {
			_ = b.cli.ContainerExecStart(ctx, k.ID, container.ExecStartOptions{Detach: true})
		}
		// force-close the attach stream so <-done unblocks even if the
		// container-side stream lingers
		resp.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}

	insp, err := b.cli.ContainerExecInspect(ctx, attach.ID)
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec inspect: %w", err)
	}
	res := ExecResult{
		ExitCode: insp.ExitCode,
		Stdout:   clip(outBuf.String()),
		Stderr:   clip(errBuf.String()),
		Duration: time.Since(start),
	}
	if timedOut {
		res.ExitReason = "timeout"
	}
	return res, nil
}

// ExecAsync starts a detached process inside the container and returns a
// handle immediately (spec §4.2 async mode).
//
// pid 语义（实现注记）：docker exec inspect 汇报的 Pid 是**宿主命名空间** pid，
// 不能用于容器内 kill。解法：命令包一层 setsid——`setsid sh -c <cmd> & echo $!`
// 由我们自己的 exec 读回**容器内** pid，Kill 用它精确打击。setsid 让命令自成
// 进程组（对齐 spec §4.2 语义，杀组 = 杀这个 pid）。
func (b *Backend) ExecAsync(ctx context.Context, id resource.ResourceID, cmd string) (*Handle, error) {
	name := b.containerName(id)
	// 读回容器内 pid：setsid 起独立会话，$! 是 setsid 后台进程的 pid
	// exit-code wrapper (D-05): the wrapper records the terminal state to
	// /tmp/.sb-exit-$$ (its own pid = the pid we capture below). Shipped via
	// base64 so cmd's quotes can never break the outer quoting (the heredoc
	// variant mangled on any cmd containing single quotes — golden-tested).
	wrap := fmt.Sprintf("#!/bin/sh\n%s\ncode=$?\n"+
		"if [ $code -gt 128 ]; then echo \"sig:$((code-128))\" > /tmp/.sb-exit-$$; "+
		"else echo \"$code\" > /tmp/.sb-exit-$$; fi\n", cmd)
	b64 := base64.StdEncoding.EncodeToString([]byte(wrap))
	pidCmd := fmt.Sprintf(`setsid /bin/sh -c 'echo %s | base64 -d > /tmp/.sb-wrap.sh && `+
		`chmod +x /tmp/.sb-wrap.sh && exec /tmp/.sb-wrap.sh' & echo $!`, b64)
	execCfg := container.ExecOptions{
		Cmd:          []string{"/bin/sh", "-c", pidCmd},
		AttachStdout: true,
		AttachStderr: true,
	}
	attach, err := b.cli.ContainerExecCreate(ctx, name, execCfg)
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}
	resp, err := b.cli.ContainerExecAttach(ctx, attach.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()
	var pidBuf bytes.Buffer
	_, _ = stdcopy.StdCopy(&pidBuf, &pidBuf, resp.Reader)
	insp, err := b.cli.ContainerExecInspect(ctx, attach.ID)
	if err != nil {
		return nil, fmt.Errorf("exec inspect: %w", err)
	}
	if insp.ExitCode != 0 {
		return nil, fmt.Errorf("async launch failed (exit %d)", insp.ExitCode)
	}
	innerPid := parseInnerPid(pidBuf.String())
	if innerPid <= 1 {
		return nil, fmt.Errorf("async launch: could not read container pid (got %q)", tail(pidBuf.String(), 100))
	}
	// identity anchor (D-05): /proc/<pid>/stat field 22 (starttime) — pid
	// reuse detection. Split AFTER the last ')' : comm may contain spaces
	// and parentheses (golden-tested).
	starttime := b.readStarttime(name, innerPid)
	exitFile := fmt.Sprintf("/tmp/.sb-exit-%d", innerPid)
	// reaper: batch-probe pattern (D-06 shape, per-handle for the docker
	// axis today — the companion supervisor generalizes it in T8).
	h := &Handle{
		ID:     attach.ID,
		EnvID:  id,
		ExecID: attach.ID,
		PGID:   innerPid,
		Cmd:    cmd,
		State:  HandleRunning,
	}
	go func() {
		poll := time.NewTicker(500 * time.Millisecond)
		defer poll.Stop()
		probe := fmt.Sprintf("kill -0 %d 2>/dev/null && echo alive || echo dead; cat %s 2>/dev/null || echo __NOEXIT__",
			innerPid, shellQuote(exitFile))
		graceTicks := 0
		for range poll.C {
			pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			r, err := b.execSimple(pctx, name, probe)
			cancel()
			if err != nil {
				if isNotFound(err) {
					// container itself is gone: the process cannot be alive
					// and the wrapper file died with it — honest unknown (D-21)
					if b.onHandleDead != nil {
						b.onHandleDead(h.ID, "exit_unknown")
					}
					return
				}
				continue // transient exec failure is not a death fact
			}
			alive := strings.Contains(r, "alive")
			exitOut := "__NOEXIT__"
			for _, ln := range strings.Split(strings.TrimSpace(r), "\n") {
				ln = strings.TrimSpace(ln)
				if ln != "alive" && ln != "dead" && ln != "" {
					exitOut = ln
				}
			}
			if alive {
				// pid reuse check: a reused pid has a different starttime
				if starttime != "" {
					if now := b.readStarttime(name, innerPid); now != starttime {
						alive = false // identity drifted: the original is gone
					}
				}
				if alive {
					graceTicks = 0
					continue
				}
			}
			// dead (or identity drifted): resolve the terminal state honestly (D-21)
			term := terminalFromExitOut(exitOut)
			if term == "" {
				graceTicks++
				if graceTicks < 6 { // ~3s grace for a racing wrapper write
					continue
				}
				term = "exit_unknown" // dead ≥ grace with no code — honest fact
			}
			if b.onHandleDead != nil {
				b.onHandleDead(h.ID, term)
			}
			return
		}
	}()
	return h, nil
}

// readStarttime reads /proc/<pid>/stat field 22. Empty string = unresolvable
// (older kernels/odd namespaces) — the reaper then falls back to kill -0
// alone rather than lying about identity.
func (b *Backend) readStarttime(name string, pid int) string {
	return b.readStarttimeFallback(name, pid)
}

func (b *Backend) readStarttimeFallback(name string, pid int) string {
	out, err := b.execSimple(context.Background(), name, fmt.Sprintf("cat /proc/%d/stat 2>/dev/null", pid))
	if err != nil {
		return ""
	}
	idx := strings.LastIndex(out, ")")
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(out[idx+1:])
	// fields[0] is state (field 3); starttime is field 22 → offset 19 here
	if len(fields) >= 20 {
		return fields[19]
	}
	return ""
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// terminalFromExitOut maps the wrapper file's content to a D-21 terminal
// state: "N" → exited(N); "-N"/"sig:N" → signaled; "__NOEXIT__" → "" (grace).
func terminalFromExitOut(exitOut string) string {
	exitOut = strings.TrimSpace(exitOut)
	if exitOut == "" || exitOut == "__NOEXIT__" {
		return ""
	}
	if strings.HasPrefix(exitOut, "sig:") {
		return "signaled(" + strings.TrimPrefix(exitOut, "sig:") + ")"
	}
	if isDigits(exitOut) {
		return "exited(" + exitOut + ")"
	}
	return "exit_unknown"
}

// SetHandleDeadHook registers the death-notification callback the orchestrator
// installs (audit event + piggyback delivery); terminal is the D-21 tri-state
// ("exited(N)" | "signaled(N)" | "exit_unknown"); nil = fire-and-forget.
func (b *Backend) SetHandleDeadHook(fn func(handleID string, terminal string)) {
	b.onHandleDead = fn
}

// execSimple runs a short command and returns trimmed stdout (internal probe
// path; not the contract surface).
func (b *Backend) execSimple(ctx context.Context, name, cmd string) (string, error) {
	execCfg := container.ExecOptions{Cmd: []string{"/bin/sh", "-c", cmd}, AttachStdout: true}
	attach, err := b.cli.ContainerExecCreate(ctx, name, execCfg)
	if err != nil {
		return "", err
	}
	resp, err := b.cli.ContainerExecAttach(ctx, attach.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", err
	}
	defer resp.Close()
	var out bytes.Buffer
	_, _ = stdcopy.StdCopy(&out, &out, resp.Reader)
	insp, err := b.cli.ContainerExecInspect(ctx, attach.ID)
	if err != nil {
		return "", err
	}
	if insp.ExitCode != 0 {
		return out.String(), fmt.Errorf("exit %d", insp.ExitCode)
	}
	return out.String(), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func parseInnerPid(out string) int {
	out = strings.TrimSpace(out)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n := 0
		if _, err := fmt.Sscanf(line, "%d", &n); err == nil && n > 1 {
			return n
		}
	}
	return -1
}

// Kill terminates an async process by container-namespace pid (from the
// handle). TERM then KILL; children die with the session leader's group.
func (b *Backend) Kill(ctx context.Context, id resource.ResourceID, pid int) error {
	_, err := b.Exec(ctx, id, fmt.Sprintf("kill -TERM -%[1]d 2>/dev/null; kill -TERM %[1]d 2>/dev/null; sleep 0.3; kill -KILL -%[1]d 2>/dev/null; kill -KILL %[1]d 2>/dev/null; true", pid),
		ExecOptions{Timeout: 5 * time.Second})
	return err
}

// WaitHandle returns the handle's current state (polling read; the push path
// is the death hook above).
func (b *Backend) WaitHandle(ctx context.Context, handleID string) (*Handle, error) {
	insp, err := b.cli.ContainerExecInspect(ctx, handleID)
	if err != nil {
		if isNotFound(err) {
			return &Handle{ID: handleID, State: HandleDead, ExitCode: -1}, nil
		}
		return nil, err
	}
	if insp.Running {
		return &Handle{ID: handleID, State: HandleRunning}, nil
	}
	return &Handle{ID: handleID, State: HandleDead, ExitCode: insp.ExitCode}, nil
}

// ErrBinary is returned when file_read hits a binary file (D-08): honest
// refusal with bounded advice — the agent has the shell escape hatch.
var ErrBinary = fmt.Errorf("binary file: file_read is text-only (bound: single file < %s); use shell_exec with base64/tar for binary content", "10MB")

// ErrContainerStopped is returned when file ops target a stopped container
// (D-22): explicit error, never a silent auto-start (start is the agent's
// chosen action via shell_exec or container lifecycle).
var ErrContainerStopped = fmt.Errorf("container_stopped: file operations need a running container; start it explicitly first")

// underWorkspace reports whether the agent path lives in the bind-mount —
// the one zone with a deterministic container→host translation (fast path).
func underWorkspace(path string) bool {
	rel := strings.TrimPrefix(filepath.ToSlash(path), WorkdirInContainer)
	return rel != "" && strings.HasPrefix(rel, "/") || rel != ""
}

// readFileViaExec reads any container-namespace path through exec (D-10).
// base64 is the binary-safe bridge; output decodes then normalizes.
func (b *Backend) readFileViaExec(ctx context.Context, name, path string) (string, error) {
	cmd := fmt.Sprintf("test -f %s || echo __NOFILE__; base64 %s 2>/dev/null | tr -d '\n'", shellQuote(path), shellQuote(path))
	out, err := b.execSimple(ctx, name, cmd)
	if err != nil {
		return "", fmt.Errorf("exec read %q: %w", path, err)
	}
	out = strings.TrimSpace(out)
	if strings.HasPrefix(out, "__NOFILE__") {
		return "", fmt.Errorf("read %q: not a regular file", path)
	}
	raw, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		return "", fmt.Errorf("read %q: decode: %w", path, err)
	}
	if file.IsBinary(raw) {
		return "", ErrBinary
	}
	return string(file.DecodeText(raw)), nil
}

// writeFileViaExec writes any container-namespace path through exec:
// stdin-free — content travels as base64 inside the command, staged to a
// same-dir temp then mv (atomicity mirror of the host fast path).
func (b *Backend) writeFileViaExec(ctx context.Context, name, path, content string) error {
	if len(content) > file.MaxFileBytes {
		return fmt.Errorf("content exceeds the %d MB file cap (D-07)", file.MaxFileBytes/(1024*1024))
	}
	b64 := base64.StdEncoding.EncodeToString(file.Normalize(content))
	dir := filepath.ToSlash(filepath.Dir(path))
	tmp := path + ".tmp-write"
	cmd := fmt.Sprintf("mkdir -p %s && echo %s | base64 -d > %s && mv %s %s",
		shellQuote(dir), shellQuote(b64), shellQuote(tmp), shellQuote(tmp), shellQuote(path))
	if _, err := b.execSimple(ctx, name, cmd); err != nil {
		return fmt.Errorf("exec write %q: %w", path, err)
	}
	return nil
}

// ensureRunning guards file ops against stopped containers (D-22).
func (b *Backend) ensureRunning(ctx context.Context, name string) error {
	insp, err := b.cli.ContainerInspect(ctx, name)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", name, err)
	}
	if insp.State == nil || !insp.State.Running {
		return ErrContainerStopped
	}
	return nil
}

// ReadFile reads a file inside the container (D-09/D-10 dual track):
// /workspace/* takes the host fast path (bind-mount translation), every
// other container-namespace path goes through exec. Binary content is
// refused with bounded advice (D-08).
func (b *Backend) ReadFile(ctx context.Context, id resource.ResourceID, path string) (string, error) {
	name := b.containerName(id)
	host, hostErr := b.hostPath(name, path)
	if hostErr == nil {
		raw, err := os.ReadFile(host)
		if err == nil {
			if file.IsBinary(raw) {
				return "", ErrBinary
			}
			return string(file.DecodeText(raw)), nil
		}
		// missing file inside a translated path: fall through to exec for a
		// namespace-truthful error (the host view may lag the container)
	}
	if err := b.ensureRunning(ctx, name); err != nil {
		return "", err
	}
	return b.readFileViaExec(ctx, name, path)
}

// WriteFile writes a file inside the container (dual track mirroring
// ReadFile); content normalized to UTF-8 LF no BOM either way.
func (b *Backend) WriteFile(ctx context.Context, id resource.ResourceID, path, content string) error {
	name := b.containerName(id)
	host, hostErr := b.hostPath(name, path)
	if hostErr == nil {
		if err := os.MkdirAll(filepath.Dir(host), 0o755); err == nil {
			data := file.Normalize(content)
			tmp := host + ".tmp-write"
			if err := os.WriteFile(tmp, data, 0o644); err == nil {
				if err := os.Rename(tmp, host); err == nil {
					return nil // fast path landed
				}
				os.Remove(tmp)
			}
		}
	}
	if err := b.ensureRunning(ctx, name); err != nil {
		return err
	}
	return b.writeFileViaExec(ctx, name, path, content)
}

// BuildOptions tunes container_build.
type BuildOptions struct {
	Tag        string // required — explicit tags prevent collision (AD-023)
	Dockerfile string // workdir-relative, default "Dockerfile"
}

// Build builds an image from a Dockerfile inside the env workdir.
// Path resolution ⊆ workdir is physics: the daemon only sees the bind-mount.
func (b *Backend) Build(ctx context.Context, id resource.ResourceID, opts BuildOptions) error {
	if opts.Tag == "" {
		return fmt.Errorf("tag is required (explicit tags prevent collision)")
	}
	name := b.containerName(id)
	dfRel := opts.Dockerfile
	if dfRel == "" {
		dfRel = "Dockerfile"
	}
	hostDF, err := b.hostPath(name, dfRel)
	if err != nil {
		return fmt.Errorf("bad_path: %w", err)
	}
	if _, err := os.Stat(hostDF); err != nil {
		return fmt.Errorf("dockerfile_missing: %q not found in workdir", dfRel)
	}
	buildCtx, err := tarDir(workdirFor(b.workHome, name))
	if err != nil {
		return fmt.Errorf("build context: %w", err)
	}
	resp, err := b.cli.ImageBuild(ctx, buildCtx, types.ImageBuildOptions{
		Tags:       []string{opts.Tag},
		Dockerfile: filepath.Base(hostDF),
	})
	if err != nil {
		return fmt.Errorf("build_failed: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(out), "\"error\"") {
		return fmt.Errorf("build_failed: %s", tail(string(out), 800))
	}
	return nil
}

// clip truncates output keeping the tail (spec §4.2: 截断留尾).
func clip(s string) string {
	const maxOut = 64 * 1024
	if len(s) <= maxOut {
		return s
	}
	return s[len(s)-maxOut:]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// OpenSession starts a persistent shell inside the container (one exec with
// AttachStdin) and returns its pipes. The caller frames commands with
// pkg/shell markers; the session's cwd/exports persist across commands.
func (b *Backend) OpenSession(ctx context.Context, id resource.ResourceID, shellCmd []string) (io.WriteCloser, io.Reader, func(), error) {
	if len(shellCmd) == 0 {
		shellCmd = []string{"/bin/sh"}
	}
	execCfg := container.ExecOptions{
		Cmd:          shellCmd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}
	attach, err := b.cli.ContainerExecCreate(ctx, b.containerName(id), execCfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("session exec create: %w", err)
	}
	resp, err := b.cli.ContainerExecAttach(ctx, attach.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("session exec attach: %w", err)
	}
	// docker multiplexes stdout/stderr on one stream when Tty=false — unwrap
	// so the drain goroutine sees pure payload
	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(pw, pw, resp.Reader)
		pw.Close()
	}()
	stdin := &stdinWriter{conn: resp.Conn}
	teardown := func() {
		resp.Close()
	}
	return stdin, pr, teardown, nil
}

// stdinWriter is the exec connection's write half as io.WriteCloser.
type stdinWriter struct {
	conn net.Conn
}

func (w *stdinWriter) Write(p []byte) (int, error) { return w.conn.Write(p) }
func (w *stdinWriter) Close() error                { return w.conn.Close() }
