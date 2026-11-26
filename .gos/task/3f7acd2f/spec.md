# Companion Spec — sandboxd-server（THINK 产物，PLAN 输入）

状态：THINK 完成（D-01~D-30，Red Team 3 轮 17 findings 全 ACCEPT）
上游：design-spec-v3.md §10.0/§10.0.1（v3.14 架构第一公民）、AD-028/030/035

## 1. Observable Truths（完工时可观察的事实）

| # | Truth | 验证路径 |
|---|---|---|
| T1 | sandboxd-server 二进制（linux/windows 双目标）scp 到 ssh 机器后单命令启动，写 discovery 文件 `~/.sandboxd-server/conn.json`（0600） | e2e：bootstrap→conn.json 存在+权限位断言 |
| T2 | sandboxd 与 companion 握手成功后，status 中该机器 `mode: companion`；companion 杀掉后自动落 `mode: thin (managed by operator)`，机器不断开 | 注入：kill companion→≤15s status 翻转 |
| T3 | companion 在场时 `shell_exec async=true` 在 ssh 机器可用，返回 handle；进程死后账本行 dead（含 exit code 或 exit_unknown），事件流可见 | e2e：async sleep→wait→账本断言 |
| T4 | ssh 机器断网（iptables drop）时：进行中 exec 报错携带 handle_id（进程仍活），≤15s 状态 disconnected；恢复后 L5 自动重接、handle 可 wait | 注入门禁 D-26 |
| T5 | file_read/write/patch/search 在 ssh 机器走协议（engine 字段/审计证明），宿主不再经 SFTP 数据路径 | 审计行+代码路径断言 |
| T6 | docker 轴容器内任意路径 file_read/write 可用（exec 通道），workspace 仍走宿主直读 fast path；响应带 durable 标志 | 表驱动契约测试 D-28 |
| T7 | 版本不匹配（解析成功）触发重装且仅限钉扎 sha256；其他哈希→thin+needs_reinstall；重装计数>2 锁 thin | 注入：假哈希二进制→thin 断言 |
| T8 | proto 一致性：双向 max_line_bytes 超限帧错误；10MB base64 载荷正常通过 | 协议 conformance 测试 D-25 |

## 2. 架构（一段话 + 图）

sandboxd（本机）—ssh 直连→（bootstrap：scp+启动）→ sandboxd-server（目标机 daemon，非特权用户）—unix socket→ 本地协议客户端。sandboxd 与 companion 的日常流量走 ssh direct-tcpip 转发到该 socket；daemon 与 ssh 连接解耦（L4）。docker 轴不部署 companion（D-03），只共享契约词汇。

```
docker:  sandboxd ──[in-proc 适配器]──> docker daemon（即"伴生"角色模板）
ssh:     sandboxd ──ssh direct-tcpip──> sandboxd-server ──> 目标机进程/文件
              └─ thin 兜底（companion 缺席/不支持 OS）：SFTP+阻塞 exec（现状）
```

## 3. 协议骨架

- 传输：unix socket（linux）/ named pipe（windows，W2 若不支持则 Windows 钉 thin——D-20 平台矩阵）
- 帧：JSON lines + request-id；双向 max_line_bytes=32MB（D-25）；握手首行 {token, proto_version, os, arch, owner_id}
- RPC：exec{blocking|async}、wait/kill(handle)、file.read/write/patch/search/stat、ping（心跳复用）、shutdown、sessions.list（L5 对账数据源）
- 事件（server→client 推送）：process.exited{handle,terminal_state}、heartbeat、log
- 幂等：exec 带 client-request-id，TTL 内去重返回已有 handle（D-19）
- 限额：10MB 载荷上限在协议层强制（D-27）、进程表 256/机（D-24）、search 硬顶 300s（D-27）

## 4. 安全不变量（全部代码强制，无文本纪律）

| 不变量 | 机制 |
|---|---|
| 无人部署钉扎 | 首部署 sha256+审批者→审计行；auto-install 仅限钉扎哈希（D-15） |
| socket 端点认证 | 随机 token 首行验证 fail-closed；目录 0700（D-16） |
| 非 root | uid0 拒启除非 allow_root:true；companion_uid 入 status（D-17） |
| 损坏降级链 | probe→adopt→thin，最重操作永不作 UNKNOWN 兜底（D-18） |
| 双实例隔离 | owner_id+token 不匹配→独立安装目录（D-18） |
| 诚实终态 | exited/signaled/exit_unknown 三态，永不伪装 0（D-21） |

## 5. 波次（D-14 修订版，吸收 R3 门禁要求）

| 波 | 内容 | 关键门禁 |
|---|---|---|
| W1 | pkg/file 析出（normalize/atomic/sensitive/decode 收拢三处重复）+ docker 非 workspace exec 通道 + durable 字段/描述更新 + search 超时参数化 + D-28 契约测试 | 全量 go test 绿；现有 e2e 不回归 |
| W2 | companion 骨架：server 二进制+握手+心跳+discovery+bootstrap+pin/token/root-guard/conn.json 原子写 | T1/T2；conn.json 损坏→thin（D-18 注入） |
| W3 | 进程 supervisor+exec/async/wait/kill+账本对账（双向：sandboxd 重启 D-06、companion 重启 D-23）+幂等+三态终态+进程表上限 | T3/T4+D-26 注入门禁；1k handles 压测 |
| W4 | 文件协议化（read/write/patch/search 协议 RPC+SFTP 降兜底）+协议 conformance+sha256 钉扎安装流 | T5/T7/T8；11GB 盘满场景 exit_unknown（D-21） |

container_build context 流式回传：W4 末评估，真需求才做。

## 6. 明确非目标（本轮不做）

- macOS/BSD companion（平台矩阵钉 thin）
- companion 高可用/多副本（单 daemon+重启自愈）
- agent 侧自动安装（信任门是人的）
- fs watch/文件锁/xattrs
- 远端 docker 管理（companion 不碰 docker）

## Decision Cards (options tables; why-not per card)

### D-01: naming + file channel
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| sandboxd-server (chosen) | paired-half naming | vscode-server symmetry; names the relation | longer name |
| sandboxd-lite | downsized sandboxd | short | lite is a quality claim that rots as features grow |
| keep SFTP for files | reuse P0 asset | zero work | sftp subsystem disabled on some sshd; second semantic copy |

why-not: sandboxd-lite — lite is a quality claim that rots as features grow; keep SFTP for files — sftp subsystem disabled on some sshd, second semantic copy.

### D-02: vscode lifecycle mechanics
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| adopt vscode semantics (chosen) | proven lifecycle | probe/handshake/discovery/reattach/TTL/GC all mapped | must adapt commit-pin to proto_version |
| invent our own | full control | fits us exactly | re-learn every vscode lesson |

why-not: invent our own — re-learn every vscode lesson the expensive way.

### D-03: docker axis without companion
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| daemon-as-companion (chosen) | docker daemon already resident peer | process registry/events/API = same role template | none identified |
| companion proxy for docker too | uniform deployment | one story | zero-value hop in a hot path |

why-not: companion proxy for docker too — zero-value hop in a hot path the daemon already owns.

### D-04: workdir fence -> safe-box
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| keep invariant, drop permission reading (chosen) | user ruling | agent freedom; durability promise precise | requires D-10 exec channel to be real |
| keep hard fence | simpler error story | fewer paths | policy constraint pretending to be physics |

why-not: keep hard fence — a policy constraint pretending to be physics, the exact lie this project exists to stop telling.

### D-05: pid truthfulness
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| handoff + starttime + pidfd-later (chosen) | layered honesty | cheap now, kernel-grade later | starttime parser needs golden tests |
| trust kill -0 alone | simplest | one probe | pid reuse -> wrong-kill (worst outcome) |

why-not: trust kill -0 alone — pid reuse turns the probe into the wrong-kill it exists to prevent.

### D-06: supervisor shape
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| one loop + process table (chosen) | N handles common | O(1) traffic; table feeds L5 reattach | table needs caps (D-24) |
| observer per handle | isolation | simple per-handle logic | amplifies on ssh; N goroutines+connections |

why-not: observer per handle — amplifies on ssh where every probe is a round-trip, not a syscall.

### D-07: large files
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| whole-file b64 + 10MB cap (chosen) | agent files are configs/code | simple envelope; honest refusal | big files need exec escape hatch |
| chunked streaming now | future-proof | no cap | protocol complexity before demand exists |

why-not: chunked streaming now — protocol complexity before demand; YAGNI with envelope reuse specified.

### D-08: binary reads
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| detect + bounded rejection advice (chosen) | context tax | honest; advice bounded (D-30) | one extra exec for legit binaries |
| auto-base64 everything | no rejection | zero friction | 100MB .so into agent context |

why-not: auto-base64 everything — a 100MB .so into agent context is the context tax; no middle path is honest.

### D-09: path freedom
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| whole-machine/namespace freedom (chosen) | machine-is-env | both axes finally symmetric | durability must be disclosed (D-11) |
| workdir-only | safety narrative | tiny surface | the fence lie again |

why-not: workdir-only files — /etc/nginx.conf is a legitimate agent target; the fence lie again.

### D-10: dual-track file channel (docker)
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| fast path + exec channel (chosen) | translation is real physics | speed where possible, freedom everywhere | two code paths to test |
| exec for everything | one path | uniform | wastes the one deterministic bridge |

why-not: exec for everything — wastes the one deterministic bridge; docker cp — two hops, breaks atomic rename.

### D-11: durability disclosure
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| durable flag + description line (chosen) | facts platform-side | zero restriction, pure truth | one field to maintain |
| warn only on non-durable writes | targeted | fewer flags | read-path surprises remain |

why-not: warn only on non-durable writes — read-path surprises remain; the flag is cheaper than the surprise.

### D-12: transport
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| unix socket + direct-tcpip + JSON lines (chosen) | daemon decoupled from connection | L4 natural; auth rides ssh; no ports | line limits must be explicit (D-25) |
| TCP + own auth | no ssh dependency | works without ssh | port mgmt + auth bootstrap = new problems |
| stdio on ssh channel | zero deploy | none | companion dies with connection = L4 broken |

why-not: TCP+own auth — new port and auth bootstrap for nothing; stdio on ssh channel — companion dies with the connection, L4 broken.

### D-13: trust model
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| semi-auto default (chosen) | deploy-time gate | human grants once; usage stays free | first-use friction |
| full-auto default | vscode parity | seamless | unattended daemon install |

why-not: full-auto default — unattended daemon install is the trust gate dissolved.

### D-14: wave slicing
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| four independent waves (chosen) | debt first, new components after | each wave verifiable+committable | longer total calendar |
| big bang | one review | none | unreleasable for weeks; red-team findings land late |

why-not: one big bang — unreleasable for weeks; red-team findings land when everything is already built.

### D-15: reinstall pinning
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| sha256 pin + cap + thin-lock (chosen) | trust gate must gate | unattended channel closed; audit row as fact | manual step on real upgrades |
| auto reinstall any version | seamless | seamless | D-02 x D-13 = permanent unattended deploy channel |

why-not: auto reinstall any version — D-02 x D-13 = a permanent unattended deploy channel; TOFU — what attackers exploit.

### D-16: socket auth
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| token + 0700 dir + fail-closed (chosen) | shared machines exist | local users cannot own the daemon | token file management |
| trust local namespace | simple | simple | any cron job becomes root-adjacent exec daemon client |

why-not: trust local namespace — any cron job becomes a client of a full exec/file daemon.

### D-17: root guard
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| reject uid0 unless allow_root (chosen) | humans paste sudo | blast radius capped; uid disclosed | one more flag |
| allow root | convenience | no flag | whole-machine daemon as root, agent-reachable |

why-not: allow root — whole-machine daemon as root, reachable by every agent; text warnings are not code.

### D-18: corrupt-state degradation
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| probe->adopt->thin + atomic conn.json + owner isolation (chosen) | UNKNOWN exists | heaviest op never the fallback; multi-daemon supported | more states to test |
| reinstall on any anomaly | one code path | simple | flaky machine -> reinstall loop killing sessions |

why-not: reinstall on any anomaly — flaky machine -> reinstall loop killing sessions; last-writer-wins — a corruption mode, not a shape.

### D-19: exec idempotency
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| request-id dedup + handle_id in L3 errors (chosen) | networks misjudge | no duplicate effects; orphans addressable | id bookkeeping |
| at-least-once raw | simple | simple | retry = second terraform apply |

why-not: at-least-once raw — retry = second terraform apply racing the first.

### D-20: platform matrix + starttime spec
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| explicit matrix + parser spec + golden tests (chosen) | /proc is a trap | wrong-kill prevented; windows honestly thin | matrix maintenance |
| implicit linux assumption | fewer docs | fewer docs | silent degradation on unsupported OS |

why-not: implicit linux assumption — silent degradation on unsupported OS; comm-split-on-space — misparse under adversarial process names.

### D-21: terminal tri-state
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| exited/signaled/exit_unknown (chosen) | files go missing | ledger never lies | consumers handle 3 states |
| code-or-0 | simple | simple | fabricated exit 0 = lie in the ledger of record |

why-not: code-or-0 — a fabricated exit 0 is a lie in the ledger of record.

### D-22: capability probing
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| probe at declare-time + container_stopped error (chosen) | sshd configs vary | facts in status; no silent container start | probe cost once per declare |
| assume defaults | no probe | no probe | disabled forwarding = undefined mode discovered mid-task |

why-not: assume defaults — disabled forwarding = undefined mode discovered mid-task.

### D-23: companion restart self-heal
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| disk-backed table + boot rescan + live-GC guard (chosen) | daemons crash | symmetric with D-06; GC cannot suicide | handle dir format to spec |
| memory-only table | simple | simple | upgrade/crash loses pgid bindings forever |

why-not: memory-only table — upgrade or crash loses pgid bindings forever; mtime GC deletes the running daemon's own directory.

### D-24: process table cap
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| hard cap 256 + typed error + stress gate (chosen) | agents loop | fd exhaustion impossible; failure is legible | legitimate batch jobs need config bump |
| uncapped | no refusal | no refusal | runaway -> fake machine-down -> reconnect storm |

why-not: uncapped — agent loop -> fd exhaustion -> fake machine-down -> reconnect storm; soft warning — text discipline, not code.

### D-25: line framing bounds
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| 32MB bidirectional + framing error + conformance test (chosen) | b64 inflates 4/3 | no silent truncation; testable | buffer memory per line |
| unbounded lines | simpler | simpler | bufio 64KB classic triple failure |

why-not: unbounded lines — bufio 64KB silent truncation, the classic triple failure; smaller cap breaks the 10MB payload.

### D-26: injection test gates
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| named harness per layer (chosen) | L1-L5 are prose otherwise | kill/iptables/mismatch scenarios executable | harness engineering |
| unit tests only | faster CI | faster CI | five layers unverified = unconstrained |

why-not: unit tests only — five unverifiable layers are unconstrained prose.

### D-27: server-side enforcement + logging posture
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| protocol-layer caps + audit trust row + quiet default (chosen) | platform holds facts | second client cannot bypass; privacy stated | companion carries policy |
| client-side only | lighter server | lighter server | any client bypasses |

why-not: client-side only — any second client bypasses the check; verbose logging — the remote owner reads agent traffic unconsented.

### D-28: durable invariant gate
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| table-driven durable test + invariant test (chosen) | reframing needs evidence | D-04 enforced by gate | test maintenance |
| narrative only | no test | no test | philosophy prose, no teeth |

why-not: narrative only — philosophy prose without teeth.

### D-29: agent-neutral status wording
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| "thin (managed by operator)" + no install instructions (chosen) | agents read status | no self-install past the human gate | none |
| "companion missing — install via ..." | helpful | helpful-looking | teaches agents to cross the trust gate |

why-not: "companion missing — install via ..." — helpfulness that teaches agents to cross the human trust gate.

### D-30: bounded advice + quoting contract
| Option | Premise | Pros | Cons |
|:--|:--|:--|:--|
| bounded text + quoting fn + contract test (chosen) | weak models obey literally | advice cannot assemble rm/tar-of-disk; no injection via printed commands | quoting fn to maintain |
| free-text advice | natural prose | natural prose | the advice IS the attack vector |

why-not: free-text advice — the advice itself is the attack vector for weak models.
