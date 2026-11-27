# Plan: companion Windows finish

> Based on spec: `.gos/task/052d6ccc/spec.md`. Implementation already landed
> and live-verified during this session (build-verify-first); waves record
> what shipped + the closure work. Evidence commands per task.

## Decision Inventory

- [x] D-31: loopback TCP transport + ssh -W → T1a, T1b, T1c
- [x] D-32: Job Objects + Git Bash sh → T2a, T2b, T2c

## Execution Waves

### Wave 1 (core implementation, both platforms)

#### T1a: Conn contract + TCP listener (transport, server side)
- **Covers**: D-31
- **Files**: pkg/companion/companion.go, cmd/sandboxd-server/main.go
- **Read first**: pkg/companion/companion.go, pkg/protocol/protocol.go
- **Approach**: Conn gains BindAddr+Port (Socket kept legacy-optional);
  Endpoint() renders dial address; BindAddr const = 127.0.0.1 only; server
  listens tcp 127.0.0.1:0, kernel-assigned port into conn.json.
- **Done when**: `grep -n "127.0.0.1" pkg/companion/companion.go` matches AND
  `go build ./cmd/sandboxd-server` exits 0
- **Depends on**: none

#### T1b: client bridge via native ssh -W
- **Covers**: D-31
- **Files**: pkg/remote/companion_client.go, pkg/protocol/protocol.go
- **Read first**: pkg/remote/companion_client.go dialConn
- **Approach**: dialConn runs `ssh host -W <endpoint>`; the nc -U helper and
  its dependency die. Numeric-or-string JSON-RPC ids (protocol bug surfaced
  by this probe) fixed in pkg/protocol.
- **Done when**: `grep -n '"-W"' pkg/remote/companion_client.go` matches AND
  `go test ./pkg/protocol ./pkg/remote` exits 0
- **Depends on**: T1a

#### T2a: supervisor process-family abstraction (proc split)
- **Covers**: D-32
- **Files**: pkg/companion/supervisor.go, pkg/companion/supervisor_test.go, pkg/companion/proc_unix.go, pkg/companion/proc_windows.go, pkg/companion/proctest_unix.go, pkg/companion/proctest_windows.go, pkg/companion/companion.go, pkg/companion/companion_test.go
- **Read first**: pkg/companion/supervisor.go Kill/pidAlive sites
- **Approach**: killProcFamily/pidAlive split behind go:build — unix group
  SIGKILL vs windows Job registry (TerminateJobObject, ActiveProcesses).
  WrapChildInJob assigns the child (KILL_ON_JOB_CLOSE orphan guard).
- **Done when**: `GOOS=windows go build ./pkg/companion` exits 0 AND
  `go test ./pkg/companion` exits 0
- **Depends on**: none

#### T2b: server fork hooks (shell discovery + wrap/rollback)
- **Covers**: D-32
- **Files**: cmd/sandboxd-server/proc_unix.go, cmd/sandboxd-server/proc_windows.go, cmd/sandboxd-server/main.go, go.mod, go.sum
- **Read first**: cmd/sandboxd-server/main.go exec_async case
- **Approach**: forkAsyncChild/shellFor/postForkChild per OS; empty shellFor
  → typed unsupported_typed; wrap failure rolls the row back and kills the
  half-child. Supported(): windows/amd64 added to the matrix.
- **Done when**: `GOOS=windows go build ./cmd/sandboxd-server` exits 0 AND
  `go test ./pkg/companion -run TestPlatformMatrix` exits 0
- **Depends on**: T1a, T2a

### Wave 2 (live verification)

#### T3a: winbuild live protocol round (R-02, truth T1)
- **Covers**: R-02
- **Files**: .gos/task/052d6ccc/evidence/winbuild-fullround.txt
- **Read first**: design/2-architecture/companion-spec.md
- **Approach**: cross-compile windows/amd64, scp to winbuild, WMI-detached
  launch (ssh-session children die — operator instruction), full protocol
  round over ssh -W: handshake/exec_async/wait/sessions.list/kill/replay.
- **Done when**: evidence file exists AND truth T1 replay receipt recorded
  (`.gos/logs/truth-receipts.md`)
- **Depends on**: T1b, T2b

#### T3b: linux regression after transport rewrite
- **Covers**: D-31
- **Files**: .gos/task/052d6ccc/evidence/winbuild-fullround.txt
- **Read first**: /tmp/opencode/e2e/ probes
- **Approach**: rerun the full e2e suite on the TCP path; all probes must
  keep their pass shape (mode-flip, reattach, tri-state, dual-track,
  ledger, sessions, persist).
- **Done when**: `go test ./...` exits 0 AND all seven probes print their OK
  banners (session log)
- **Depends on**: T1b

### Wave 3 (closure)

#### T4a: spec/doc sync + changelog
- **Covers**: D-31, D-32
- **Files**: design/2-architecture/companion-spec.md, docs/design-spec-v3.md, CHANGELOG.md
- **Read first**: .gos/task/052d6ccc/spec.md
- **Approach**: companion-spec D-20 matrix row windows/amd64 → supported;
  transport section: loopback TCP + ssh -W (nc dep removed); bootstrap
  gains the windows session-detach instruction; design-spec 修订行 v3.32;
  CHANGELOG Fixed (numeric-id hang) + Changed (transport).
- **Done when**: `grep -n "windows/amd64" design/2-architecture/companion-spec.md` matches AND `grep -n "v3.32" docs/design-spec-v3.md` matches
- **Depends on**: T3a, T3b

#### T4b: REVIEW → VERIFY → SHIP chain
- **Covers**: R-02
- **Files**: CHANGELOG.md, .gos/task/052d6ccc/delta.md
- **Read first**: skills/gos-review, skills/gos-verify, skills/gos-ship
- **Approach**: independent review pass on the full diff; gate all; archive
  with delta.md; push.
- **Done when**: `gos gate all` exits 0
- **Depends on**: T4a

## Truths
- T1: on winbuild — bootstrap writes conn.json (loopback auto port); ssh -W
  handshake acks; exec_async echo reaches exited(0); kill on running sleep
  answers killed=true then signaled. [VERIFIED, replay receipt]
