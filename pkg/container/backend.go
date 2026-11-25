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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

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
	onHandleDead func(handleID string, exitCode int)
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
	pidCmd := fmt.Sprintf(`setsid /bin/sh -c %s & echo $!`, shellQuote(cmd))
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
	h := &Handle{
		ID:     attach.ID,
		EnvID:  id,
		ExecID: attach.ID,
		PGID:   innerPid,
		Cmd:    cmd,
		State:  HandleRunning,
	}
	// reaping: the launch exec already exited — watch the container-namespace
	// process itself (kill -0). 500ms polling bounds death-notification latency.
	go func() {
		poll := time.NewTicker(500 * time.Millisecond)
		defer poll.Stop()
		probe := fmt.Sprintf("kill -0 %d 2>/dev/null && echo alive || echo dead", innerPid)
		for range poll.C {
			pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			r, err := b.execSimple(pctx, name, probe)
			cancel()
			if err == nil && strings.TrimSpace(r) == "dead" {
				if b.onHandleDead != nil {
					b.onHandleDead(h.ID, 0)
				}
				return
			}
		}
	}()
	return h, nil
}

// SetHandleDeadHook registers the death-notification callback the orchestrator
// installs (audit event + piggyback delivery); nil = fire-and-forget.
func (b *Backend) SetHandleDeadHook(fn func(handleID string, exitCode int)) {
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

// ReadFile reads a file inside the workdir.
func (b *Backend) ReadFile(ctx context.Context, id resource.ResourceID, path string) (string, error) {
	host, err := b.hostPath(b.containerName(id), path)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(host)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", path, err)
	}
	return string(decodeText(raw)), nil
}

// WriteFile atomically writes a file inside the workdir (temp + rename within
// the same directory), content normalized to UTF-8 LF no BOM.
func (b *Backend) WriteFile(ctx context.Context, id resource.ResourceID, path, content string) error {
	host, err := b.hostPath(b.containerName(id), path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data := normalizeUTF8LF(content)
	tmp := host + ".tmp-write"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	return os.Rename(tmp, host) // atomic within the same filesystem
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
