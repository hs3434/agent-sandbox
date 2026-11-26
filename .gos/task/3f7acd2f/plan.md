# Plan: sandboxd-server companion (P1 first battle)

> **Date**: 2026-11-26 | **Based on spec**: .gos/task/3f7acd2f/spec.md (D-01~D-30, red team 3 rounds)
> Truths: T1-T5 (defined; verified in VERIFY stage)

## Decision Inventory

- [x] D-01 naming + files via protocol + SFTP bootstrap + thin fallback → T2, T9
- [x] D-02 vscode lifecycle mechanics → T5, T6, T10, T11
- [x] D-03 docker axis no companion; contract vocabulary → T1, T7
- [x] D-04 workdir safe-box reframing → T1, T7
- [x] D-05 pid truth (handoff/starttime/wrapper exit) → T6
- [x] D-06 supervisor loop + process table + restart reconciliation → T6, T7, T8, T11
- [x] D-07 10MB cap → T9
- [x] D-08 binary detect + bounded advice → T9
- [x] D-09 path freedom both axes → T1, T7
- [x] D-10 docker exec channel + fast path + search timeout param → T1
- [x] D-11 durable flag + description honesty → T1
- [x] D-12 transport (unix socket + direct-tcpip + JSON lines + events) → T4
- [x] D-13 trust model semi-auto → T5
- [x] D-14 four waves → this plan's wave structure
- [x] D-15 reinstall pinning sha256 → T5, T10
- [x] D-16 socket token auth → T4
- [x] D-17 root guard → T5
- [x] D-18 conn.json atomic + degrade chain + owner isolation → T4, T5
- [x] D-19 exec idempotency + handle_id in L3 errors → T6
- [x] D-20 starttime parser spec + os/arch handshake + platform matrix → T4, T6
- [x] D-21 terminal tri-state → T6
- [x] D-22 capability probe + container_stopped → T1, T7
- [x] D-23 companion boot rescan + live-pid GC guard → T8
- [x] D-24 process cap 256 + stress gate → T8
- [x] D-25 max_line_bytes framing + conformance test → T4
- [x] D-26 injection test harness → T10
- [x] D-27 protocol-layer caps + search ceiling + audit trust row + quiet logging → T5, T9
- [x] D-28 durable invariant contract gate → T1
- [x] D-29 agent-neutral status wording → T10
- [x] D-30 bounded advice + quoting fn + contract test → T5

## Requirement Claims

- [x] R-01: companion intent (from gos init) → all tasks

## Patterns to Follow

- Packages: pkg/<domain>/ one package per concept (see pkg/shell for session+search+dialect precedent)
- Tests: co-located *_test.go, table-driven where D-NN says "golden/table"
- Errors: fmt.Errorf with %w wrapping; typed errors for protocol layer
- e2e probes: /tmp/opencode/e2e/*.py (MCP stdio JSON-RPC pattern)

## Execution Waves

### Wave 1 — debt consolidation + docker freedom (T1 → T2 in order — F10 same-file collision)

#### T1: extract pkg/file semantic library + docker exec-channel + durable flag
- **Covers**: D-04, D-08, D-09, D-10, D-11, D-22, D-28
- **Files**: pkg/file/file.go, pkg/file/file_test.go, pkg/container/backend.go, pkg/container/backend_test.go, internal/orchestrator/face.go, internal/mcpserver/tools.go, internal/mcpserver/server.go, pkg/remote/sftp.go
- **Read first**: pkg/container/backend.go:110-170 (hostPath + normalizeUTF8LF), pkg/remote/sftp.go (its copies of normalize/decode), design/2-architecture/companion-spec.md §D-10/D-11/D-28
- **Approach**: move semantic funcs verbatim into pkg/file (normalize/decode from container backend, sensitive warnlist from mcpserver tools.go:210); docker ReadFile/WriteFile/Patch gain path branch: /workspace/* → hostPath fast path (unchanged), else exec channel (cat for read; tee to tmp+mv for write; sed-free read-modify-write via exec for patch). Binary sniff (NUL byte in first 8KB) → typed ErrBinary with bounded advice text (D-30 wording). Responses add durable: bool (workspace=work_home subtree for docker; always true for ssh — probe-verified). Stopped-container file ops → typed ErrContainerStopped (status probe first, no auto-start). Table-driven contract test for durable flags + workdir invariant (D-28). FileSearch timeout_seconds param with server-side ceiling later (W2), client clamp 300s now.
- **Done when**: `grep -c "normalizeUTF8LF" pkg/container/backend.go` = 0 (moved to pkg/file) AND `go test ./pkg/file/ ./pkg/container/ ./internal/... ` green AND new table test `TestDurableFlagContract` passes AND e2e probe writes/reads /etc/probe.txt inside container successfully
- **Depends on**: none

#### T2: thin-mode honest mode reporting (prerequisite plumbing for fallback)
- **Covers**: D-01, D-29
- **Files**: internal/orchestrator/face.go, cmd/sandboxd/adapters.go, internal/mcpserver/server.go
- **Read first**: internal/orchestrator/face.go Status section, D-29 in spec
- **Approach**: machine payload gains mode: "thin" (today always, companion lands in W2+); wording "thin (managed by operator)". No install instructions anywhere in descriptions (grep gate).
- **Done when**: e2e status shows mode:thin AND `grep -ri "install" internal/mcpserver/server.go` has 0 hits in tool descriptions
- **Depends on**: none

### Wave 2 — protocol + docker ledger API (parallel)

#### T4: protocol package + framing + conformance test
- **Covers**: D-12, D-16, D-20, D-25
- **Files**: pkg/protocol/protocol.go, pkg/protocol/auth.go, pkg/protocol/protocol_test.go
- **Read first**: pkg/shell/session.go (marker discipline precedent), D-12/D-16/D-25 in spec
- **Approach**: codec over io.ReadWriter; bufio.Reader with explicit limit (no default Scanner); request registry map[request-id]chan for multiplexing; handshake message validated fail-closed before any method dispatch.
- **Done when**: `go test ./pkg/protocol/` green with conformance tests (oversize→ErrFraming, 10MB roundtrip, F12: 100 goroutine-issued distinct request-ids over ONE connection, response correlation map asserted, zero mismatches/drops)
- **Depends on**: none (F5: constants are protocol-owned)

#### T5: companion server binary — lifecycle, security, bootstrap
- **Covers**: D-01, D-02, D-13, D-15, D-16, D-17, D-18, D-27, D-30
- **Files**: cmd/sandboxd-server/main.go, pkg/companion/discovery.go, pkg/companion/bootstrap.go, pkg/companion/bootstrap_test.go, internal/config/config.go
- **Read first**: pkg/protocol/* (from T4), pkg/remote/backend.go (existing ssh plumbing to reuse), D-15/D-16/D-17/D-18/D-27/D-30 in spec
- **Approach**: main.go is a thin composition root (mirror cmd/sandboxd style); discovery read is lenient (corrupt → report state, never trigger install); trust row (sha256+approver) written to audit ledger at pin time (D-27); start command printed via shell.Quote function.
- **Done when**: e2e: scp binary to loopback target, human-confirmed start (test auto-confirms via config flag in test env), conn.json exists with 0600 + token; corrupt conn.json → probe reports thin, no install attempt; uid0 start refused without allow_root; kill -9 sandboxd-server → service unit restarts within 5s; TTL test: spawn sleep 60 + disconnect + advance past TTL → server alive until handle completes; `go test ./pkg/companion/` green
- **Depends on**: T4, T2 (T2's mode enum — F6)

#### T6: sandboxd-side companion client + machine mode flip (exec-only routing)
- **Covers**: D-02, D-20 (matrix), D-21 consumption (uses T7's MarkHandleDead API on disconnect — F4)
- **Files**: pkg/remote/companion_client.go, internal/orchestrator/face.go, cmd/sandboxd/adapters.go
- **Read first**: pkg/remote/backend.go (ControlMaster state machine), pkg/protocol/*, T7's MarkHandleDead signature
- **Approach**: client wraps protocol.Codec over the forwarded stream; L2 state machine (connected/disconnected/reconnecting) with L3 fail-fast errors carrying handle_id when present + mark interrupted rows exit_unknown via T7 API; handshake reports os/arch → matrix check (linux ok, else stay thin with companion_supported:false).
- **Done when**: e2e loopback machine flips mode enum thin→companion after companion start; killing companion process → fake-clock unit test ≤15s + e2e mode flips back to thin, next exec gets immediate clear error (not timeout); file op on companion machine → typed not-yet-supported; `go test ./pkg/remote/` green
- **Depends on**: T5, T7

### Wave 3 — companion server binary

#### T7: container-side async parity fixes

> Wave 2 (parallel with T4/T5/T6) (docker axis contract alignment)
- **Covers**: D-03, D-05 (docker side), D-06 (docker side)
- **Files**: pkg/container/backend.go, pkg/container/backend_test.go
- **Read first**: pkg/container/backend.go ExecAsync/reaper (lines ~388-470), D-05/D-21
- **Approach**: three-state terminal (exited(code)|signaled(sig)|exit_unknown); ledger MarkHandleDead gains terminal-state param (schema/API lands HERE — F4: T6 consumes it); sandboxd restart reconciliation: on boot scan running handle rows → probe → resolve (D-06 sandboxd side). Done-when matrix (F13): exit 0 → exited(0); child killed -9 → signaled(137); sandboxd restarted while proc alive → exit_unknown until reconcile.
- **Done when**: `go test ./pkg/container/` green incl. golden starttime tests + tri-state matrix test; e2e async ledger shows real exit code for `sh -c 'exit 7'`; docker rm -f mid-run → exit_unknown (not 0)
- **Depends on**: none (F14: exit wrapper is in-container sh + ledger scan — no pkg/file symbols consumed)

#### T8: companion supervisor — process table, async/wait/kill, caps

> Wave 5 (after T8 deps)
- **Covers**: D-06, D-19, D-21, D-23, D-24
- **Files**: pkg/companion/supervisor.go, pkg/companion/supervisor_test.go, cmd/sandboxd-server/main.go
- **Read first**: T4 protocol, T5 server main, pkg/container/backend.go ExecAsync (the pattern being ported), D-06/D-19/D-21/D-23/D-24
- **Approach**: exec-async carries client-request-id → TTL map dedup (return existing handle); wait/kill address by handle_id; L1 heartbeat piggybacks supervisor tick (no extra goroutine); sandboxd restart reconciliation for companion rows goes through sessions.list (D-06 both sides).
- **Done when**: e2e: async sleep on loopback machine → handle → ledger dead with honest code; kill -9 companion + restart → boot rescan rebuilds table, prior handles waitable; 257th concurrent async → typed cap error; same request-id twice → same handle
- **Depends on**: T5, T6, T7

### Wave 4 — client + machine mode flip


#### T9: file protocolization (companion serves pkg/file semantics)

> Wave 6
- **Covers**: D-01, D-07, D-08, D-09, D-27
- **Files**: pkg/companion/filesvc.go, cmd/sandboxd-server/main.go, pkg/remote/companion_client.go, internal/mcpserver/tools.go
- **Read first**: pkg/file/* (from T1), T6 client, D-01/D-07/D-08/D-27
- **Approach**: companion imports pkg/file — one semantic implementation, three transports (host-direct, protocol, SFTP fallback). Search runs rg→grep in-process on target.
- **Done when**: e2e: file_write/read/patch/search on loopback machine → audit trail shows protocol path (mode:companion), durable flags present; 11MB write → protocol cap error; binary read → typed rejection with bounded advice; stop companion → file ops still work via SFTP fallback (thin)
- **Depends on**: T8

#### T10: red-team gate harness + reinstall pinning e2e + wording sweep

> Wave 7 (final)
- **Covers**: D-02, D-15, D-26, D-29, D-30
- **Files**: docs/verification/feature-test-matrix.md, internal/mcpserver/server.go
- **Read first**: D-26 list (the three named scenarios), D-15 cap semantics, truths T1-T5
- **Approach**: harness is a shell script per scenario asserting observable state (status/ledger/events via MCP stdio), exit codes as gate evidence; matrix rows reference script names; iptables scenario asserts wall-clock <20s (CI margin over the 15s budget — F7).
- **Done when**: all named injection scenarios pass; `gos gate coverage` exit 0; truths T1-T5 verifiable with these scripts as artifacts
- **Depends on**: T9, T11

#### T11: client L5 reattach + ledger reconcile (companion half of D-02/D-21)

> Wave 6
- **Covers**: D-02 (reattach mechanism), D-06 (reconcile via sessions.list), D-21 (tri-state on reattach)
- **Files**: pkg/remote/companion_client.go, internal/orchestrator/face.go
- **Read first**: T6 client, T8 sessions.list, T7 MarkHandleDead
- **Approach**: F3 — reattach reconcile is a distinct mechanism from connection-level L2 reconnect; this task owns it. Service unit auto-restart (T5) brings the companion back; client reconcile brings the ledger back.
- **Done when**: e2e: async handle → kill -9 companion → service restarts → client reattaches → sessions.list reconciles → handle still waitable, ledger honest; sandboxd restart mid-run → same reconcile path on boot
- **Depends on**: T8

## Out of Scope (explicit)

- Shell sessions over companion (F11 scope decision: machines keep sessions via thin exec transport even at mode:companion — session.stream/pty deferred until real demand; silence converted to explicit decision)
- Windows companion (matrix pins thin; named pipe future)
- container_build context streaming (evaluate at W4 end, only on real demand)
- macOS/BSD companions; fs watch; file locks; xattrs; remote docker management
