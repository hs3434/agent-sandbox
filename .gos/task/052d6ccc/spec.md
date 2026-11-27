# Spec: companion Windows finish (winbuild-proven)

## Intent
Prove the companion stack on windows/amd64 against the winbuild host
(192.168.90.253), keeping ONE binary + ONE protocol surface for linux and
windows (v3.14 architecture promise; AD-036 continuation).

## Requirements
- R-01 (init): companion windows finish — prove sandboxd-server windows
  target on the winbuild host; named-pipe transport or honest thin pin;
  windows bootstrap/start instruction.
- R-02: windows companion parity — sandboxd-server windows/amd64 serves the
  same protocol surface as linux over loopback TCP + ssh -W, verified live
  on winbuild.

## Decisions (delta against companion-spec.md)
- **D-31 transport rewrite**: daemon binds `127.0.0.1:<kernel-assigned port>`
  (companion.BindAddr), port recorded in conn.json (`bind_addr` + `port`
  fields; legacy `socket` kept optional), sandboxd bridges via native
  `ssh -W host`. Rationale: the unix-socket era forced an `nc -U` stdio
  bridge; windows has no nc (verified on winbuild), and `ssh -W` is TCP-only
  — so the loopback TCP endpoint removes the helper dependency on BOTH
  platforms and keeps the original transport decision's real invariants (auth rides ssh + token handshake;
  daemon decoupled from connection L4; zero port management via kernel
  assignment). Conn discovery restart-drift closes itself: connect() re-fetches
  conn.json before every dial (5s cache window, honest disconnected between).

### D-31: windows transport bridge
| Option | Premise | Pros | Cons | Activates |
|---|---|---|---|---|
| loopback TCP + ssh -W (chosen) | OpenSSH 9.5 on windows ships -W; TCP is universal | zero remote helper (nc dep removed on linux too); one code path both platforms | conn.json gains port field; listener bound to loopback is the security gate | — |
| Go named-pipe + stdio bridge subcommand | winio vendored, bridge ships in the same binary | keeps unix-socket shape | ~100 lines winio + bridge mode; TCP-only ssh -W can't reach pipes anyway — needs nc-equivalent again | only if ssh -W shows a real limitation |
| windows pins thin (spec's honest retreat) | the platform matrix allowed it | zero code | companion's core value absent on windows; winbuild proving ground wasted | D-31 proven impossible |
why-not: Go named-pipe + stdio bridge subcommand — re-adds the remote-helper dependency D-31 exists to remove; windows pins thin (spec's honest retreat) — fallback only if loopback TCP fails live (it didn't).

- **D-32 windows process model**: Job Objects + Git Bash `sh.exe` wrapper.
  The Job Object is the kernel twin of the unix process group: kill-tree
  (TerminateJobObject), KILL_ON_JOB_CLOSE orphan guard (crashed daemon never
  leaves agent processes), ActiveProcesses>0 liveness probe. Wrapper shell =
  Git Bash sh.exe (winbuild reality); missing shell answers typed
  `unsupported_typed` instead of forking blind. Platform split = 4 functions
  x ~50 lines behind `go:build` tags (proc_unix/proc_windows, server +
  supervisor + test child); protocol, supervisor ledger, file service,
  transport, discovery stay one codebase.

### D-32: windows process model
| Option | Premise | Pros | Cons | Activates |
|---|---|---|---|---|
| Job Objects + Git Bash sh (chosen) | winbuild has Git for Windows; MSYS pids not kernel pids | kill reaches whole tree; KILL_ON_JOB_CLOSE = orphan guard; 4 funcs x 50 lines split | requires Git Bash present (typed refusal otherwise); pid-recycle fallback debt until pidfd endgame | — |
| MSYS kill -9 single-pid | bash children are MSYS-managed | tiny change | kills leader only — grandchildren orphaned; cygwin pid ≠ winpid breaks identity | never (wrong-kill class) |
| exec_async pins thin on windows | Job wrap deferred | fastest | async is companion's core value; half-companion misleads callers | only if Git Bash absent fleet-wide |
why-not: MSYS kill -9 single-pid — wrong-kill class, grandchildren orphaned; exec_async pins thin on windows — removes the core feature.

- **Platform matrix extension**: windows/amd64 now `Supported` (was honest-thin);
  windows/arm64 and darwin stay thin. Golden test updated.
- **Bootstrap semantics (windows reality)**: processes started under an SSH
  session die with the session — the daemon must be started session-detached
  (WMI `Win32_Process.Create` proven on winbuild; scheduled task is the
  production-grade variant). This is an operator/bootstrap instruction, not
  daemon code.

## Bugs fixed en route
1. protocol.ReadMessage rejected numeric JSON-RPC ids (`id` was `string`) —
   any numeric-id client hung silently then the conn dropped. Now
   `json.RawMessage` both-typed with number normalization (caught by the
   D-31 TCP probe; the old unix-socket path never met it).
2. TestPlatformMatrix asserted windows==thin; updated for the extended
   matrix (windows/amd64 required, windows/arm64 + darwin thin).

## Observable truths
- T1 (verified, replay receipt): on winbuild — bootstrap writes conn.json
  (loopback auto port); ssh -W handshake acks; exec_async echo reaches
  exited(0); kill on running sleep answers killed=true then signaled.
  Evidence: .gos/task/052d6ccc/evidence/winbuild-fullround.txt

## Verification summary
- Unit: go test ./... green (all packages, both builds compile:
  GOOS=linux + GOOS=windows/amd64).
- Linux e2e (transport rewrite regression): t6_modeflip, t11_reattach,
  t7_tristate, t1_dualtrack, async_ledger, sess_full, persist — all OK on
  the new TCP loopback path (nc dependency gone).
- Windows live: full protocol round via ssh -W (handshake/exec_async/wait/
  sessions.list/kill/idempotent-replay) — see T1 evidence.

## Deferred (named, not silent)
- named-pipe transport: unnecessary under D-31 (loopback TCP already
  loopback-local); revisit only if ssh -W shows a real limitation.
- pidfd starttime endgame on windows: Job registry is primary truth; pid
  fallback documented in proc_windows.go.
- daemon auto-start as a windows service (nssm/scheduled task): operator
  choice; bootstrap instruction documented, not installed by us.
