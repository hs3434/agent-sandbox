# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- Add T4-T11: sandboxd-server companion (P1 first battle) — protocol package (JSON lines + fail-closed token handshake + 32MB framing), companion lifecycle pkg (atomic conn.json 0700/0600, token auth, root guard, owner-socket isolation, probe-adopt-thin degrade chain), sandboxd-server binary (exec_async/wait/kill/sessions.list/file.read/file.write + events push), supervisor (disk-backed table, boot rescan, 256 cap, request-id idempotency, honest tri-state), companion client (ssh nc -U bridge + L2 state machine + mode flip), file protocolization (companion-first with typed refusals, thin transport fallback), L5 reattach (sessions.list boot reconcile, process_wait), red-team gate 5 scenarios (R-01) [2026-11-26]
- Add T7: docker async tri-state ledger — /proc starttime identity vs pid reuse, base64 exit-code wrapper (quote-proof), restart reconciliation on boot; death hook carries terminal state never fabricated 0 (R-01) [2026-11-26]
- Add T2: machine mode enum thin/companion/degraded wired into status with agent-neutral wording per D-01/D-29 (R-01) [2026-11-26]
- Add T1: pkg/file semantic library extracted as the one implementation for all transports, docker file ops dual-track per D-09/D-10, durable flag per D-11, binary refusal per D-08, container_stopped per D-22, search timeout ceiling per D-27 (R-01) [2026-11-26]

<!-- New entries accumulate under [Unreleased] and are promoted into a version
     section at release time. For earlier history, see `git log` and the sealed
     `.changes.md` archive. -->
