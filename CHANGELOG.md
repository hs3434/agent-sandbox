# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- Secure companion channel integrity — process-table cap counts concurrent forks with terminal-row GC, idempotent retries never double-fork, file search bounded by a server-side deadline, rg/grep argv option-injection closed, and disconnect recovery routed through a single pump reader so reattach converges (D-24/D-19/D-27/D-21) [2026-11-27]

- Add companion exit wrapper so natural death records its own code via a per-handle exit file, an idle-retirement TTL gate that running handles veto, target-local file patch and search inside the daemon, a systemd user unit, and disconnect-injection coverage with sub-budget recovery (D-05/D-27) [2026-11-26]
- Add sandboxd-server companion stack — protocol package with fail-closed token handshake and 32MB framing, lifecycle pkg with atomic conn files and owner-socket isolation, daemon binary with exec_async/wait/kill/sessions.list/file.read/file.write plus events push, supervisor with disk-backed table and honest tri-state terminal resolution, client with ssh bridge and mode flip, L5 reattach via boot reconcile (D-01/D-02/D-05/D-06/D-19/D-24) [2026-11-26]
- Add docker async tri-state ledger with /proc starttime identity against pid reuse and exit-code wrapper so death hooks carry terminal states never fabricated (D-05/D-21) [2026-11-26]
- Add T2: machine mode enum thin/companion/degraded wired into status with agent-neutral wording per D-01/D-29 (R-01) [2026-11-26]
- Add T1: pkg/file semantic library extracted as the one implementation for all transports, docker file ops dual-track per D-09/D-10, durable flag per D-11, binary refusal per D-08, container_stopped per D-22, search timeout ceiling per D-27 (R-01) [2026-11-26]

<!-- New entries accumulate under [Unreleased] and are promoted into a version
     section at release time. For earlier history, see `git log` and the sealed
     `.changes.md` archive. -->
