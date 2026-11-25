# Design Spec v3 — Agent 操作现实的支点（原"Sandbox 平台"）

> 状态：v3.23 — 2026-11-25，**P0 施工基线**
> 修订：v3.1 恢复边界 = 本地态；v3.2 capability = 平台原语、用例中立；v3.3 spec 即预留；v3.4 第一性重定位（赋能操作，身体/手/支点）+ 快照策略（标准镜像优先）；v3.5 身体自治（心智归 Agent）；v3.6 状态文档归本体（MCP 直连，记手的状态）；v3.7 commit 快照暂缓 + baseline=镜像引用 + 句柄持久化 + 凭证同钟；v3.8 status 工具（本体感觉与记忆分离）；v3.9 记忆键去 session 化；v3.10 记忆 = 单文档无键；v3.11 Agent 自建凭证归属澄清；v3.12 凭证子系统降级 P2；v3.13 奥卡姆清扫（P0 零快照存储）；v3.14 远程伴生服务（协议对话 + 断线五层）；v3.15 身体事件流（拉模式）；v3.16 description 指引；v3.17 响应捎带通知；v3.18 进程手收缩；v3.19 大落盘：shell_exec 双模式（async 并入）、revert 出局（不变量+键控兑现自愈）、image_build 诞生、workdir 双路径部署、DNS 网络、跟随式默认、env 元工具出局、ssh 瘦模式最小过渡、backend 双轴矩阵；v3.20 Python 对照终审：生命周期动词回归（sleep/wake）、machine 参数退场、控制面重启对账、sudo 管道注记、file 族底层特性三抄（原子写/归一化/敏感路径警告）；v3.21 scheme 再审：sleep/wake 移除（上下文税，随配额执行器 P1 回归）、image_list 补缺、machine 参数回归（P0 含 ssh——注册走配置声明）、policy/snapshot/set_default 挪 P2 + machine_list 并入 status + machine_connect 条件触发；v3.22 族重排：docker 专属面归位（container 族 4 工具以被操作物命名）、resource_inspect 并入 sense 族、companion 远程操控预留撤销；v3.23 ssh 连接自愈：ControlMaster 三件套 + 僵尸往返探测 + 会话卫生（§10.0.1，AD-028）
> 依据：云厂商资源管理哲学（AWS/Azure/GCP/阿里云/腾讯云收敛）+ Agent 执行场景分析
> 核心哲学：
> 1. **第一性 = 赋能操作**：给 AI 身体（执行环境）和手（操作原语），让 AI 像人一样操作现实
> 2. **能力释放 + 自愈兜底**：不拦命令（拦截 = 废了手）；恢复优先是为**防止自我崩溃**（自愈），非防外敌
> 3. 一切皆资源，治理统一，操作分化（capability 声明）
> 4. 控制面/数据面分离
> 5. 执行环境不可变（update = 重建，与回滚天然一致）
> 6. 恢复按"状态可重建成本"分层：快照 / 版本化 / 注入
> 7. 凭证最小暴露：永不落快照、结构上不存在 read
> 8. 审计 = 复盘工具（Agent 自省），不仅是合规记录
> 9. 配额 = 保活机制（防单任务饿死系统），非能力限制

## 0. 第一性：AI 操作现实的支点

本项目的意义 = 作为 **AI 操作现实的支点**。给 AI 一个**身体**（执行环境——可存在、
可操作、可自愈）和**手**（操作原语——shell/file/exec/snapshot），让 AI 像人一样在
各平台开发、操作系统。

- **支点**：AI 作用于现实的着力点。支点本身不多做，但它让 AI 的能力有了施展之处
- **身体**：exec_env —— 可操作、可恢复、可审计的躯壳
- **手**：capability —— 操作原语，每只手诚实声明它能做什么（容器有 shell、物理机无快照）
- **恢复与安全是次优先级**：本质是为更好更安全地操作、**防止自我崩溃**（自愈），
  不是为了隔离/防御——那属于"沙箱"框架的命题，不是本项目的
- **心智归 Agent（AD-008）**：进程管理、睡眠/唤醒、自状态文档由 Agent 自主决定——
  平台只管身体的持久性与事实，不替 Agent 记忆、不替 Agent 决策

> 与"沙箱"的关系：传统 sandbox = 拦截、限制（笼子，防外敌）。本项目不用这个框架——
> 身体框架下，Agent 是**主体**而非威胁，安全命题是"自愈"而非"防御"。
> 仓库名 `agent-sandbox` 保留（生态语境准确：E2B/Daytona 同义）；品牌名另定（见内部文档）。

## 1. 总体分层

```
控制面（MCP API + 资源模型 + 治理）→ 编排层（生命周期/恢复/归属）→ 数据面（本地执行器 / 远程伴生服务）
                                                                        ↑ 协议对话（§10.0）
```

- 控制面/数据面分离：Agent 只见资源模型，不见底层实现
- 编排层承载跨资源治理：恢复调度、配额、任务归属
- 数据面只做执行 + 上报（结果、指标、状态事件）；远程目标由伴生服务（Go 静态
  二进制）承载数据面语义——本地/远端同构程序，协议对话而非隧道透传（§10.0）

## 2. 资源模型

### 2.1 资源标识

```
resource_id = "{tenant}:{type}:{name}"
例: hu:exec_env:rnaseq-worker-01 / hu:policy:net-default / hu:credential:db-password / hu:snapshot:before-deg
```

### 2.2 资源类型注册表（P0 仅 exec_env 有消费者；"一切皆资源"统一在数据层——
注册表/ID 语法/审计——不在动词层；P1 起 policy 等以兄弟工具分化，见 AD-022）

| type | capabilities | 生命周期 | 恢复机制 | 阶段 |
|---|---|---|---|---|
| exec_env | shell/files（capability = 平台原语，见 §10） | 运行面状态机 | wake/同名重建/workdir 不变量 | **P0** |
| policy | rule 读改写 | 配置版本化 | 版本化回滚 | P1 |
| credential | inject/recycle | 注入↔回收 | 重新注入 | P2 |
| snapshot | 只读元数据 | 创建→保留→轮转 | —（本身即恢复手段） | P1 |

### 2.3 元数据（共享）

```yaml
resource_id, type, capabilities, backend, status,
tags: {tenant, project, purpose},
created_at, owner(任务标识，客户端语义)
```

limits（cpu/memory/pids）属于 exec_env 的创建规格（ExecEnvSpec，不可变），
不在共享元数据层——policy/quota 类资源没有 limits 语义。

## 3. 生命周期状态机

### 3.1 执行环境（运行面）

```
created → starting → running → stopping → stopped → removing → removed
                     └→ error（超时/失败，可重试或重建）
```
- 迁移幂等（重复调用同状态）
- 超时驱动：starting 30s / stopping 60s
- **睡眠/唤醒（AD-008 哲学保留，工具面 P1 回归 AD-026）**：sleep = graceful stop
  （stopped，容器可写层保留，文件系统原样）；wake = start **续用**（非重建，进程不
  复活）——比同名重建（自愈）便宜的日常续用通道。P0 无配额执行器（睡眠省执行时长
  无消费者）且 ssh 物理机无"睡机器"语义（docker backend 专属）——动词随配额执行器
  P1 回归；P0 生命周期迁移由平台内部驱动（docker backend 自愈重建走 stop 路径）
- **唤醒失败降级链**：容器丢失（宿主重启/docker prune）→ wake 报错 → 自动走重建链
  （标准镜像 + workdir 不变量自动重挂 + 自状态文档仍在（MCP 直连，见 §4.1）——降级不失忆）
- 策略时限默认仅计 running（睡眠不烧执行时长，占磁盘；磁盘配额 P1）
- 无 update：改镜像 = 同名重建（不可变基础设施，见 §7.2 自愈语义）
- **并发仲裁（P0 必需）**：每个 exec_env 持有编排层互斥锁——同一时刻只允许一个生命周期
  迁移或自愈重建在途；第二个操作立即拒绝（409 conflict，非排队）。自愈重建（remove +
  同名 create）视为一个临界区。锁随 removed 释放；error 状态持有锁直到显式重建
- **控制面重启对账（reconcile，v3.20——继承 sandbox-mcp _reattach_existing）**：
  sandboxd 重启 ≠ 身体丢失——容器还在 daemon 里跑，注册表有记录，缺的只是桥接。
  启动时对账：SQLite 注册表 × daemon 实际容器逐个比对——① 双方在 → 重新接管
  （重建句柄桥，状态从容器事实校准）；② 注册表有/daemon 无 → 标 error（走唤醒
  降级链/自愈）；③ daemon 有/注册表无 → 孤儿容器（按命名约定收编或列出待决，
  不静默删除）。对账是幂等只读 + 必要的状态修正，永不销毁 workdir

### 3.2 配置（版本化）
```
created(v1) → modified(v2) → ... → revert(v1) 生成新版本
```
### 3.3 凭证

```
injected → active → recycled（任务结束）/ expired（超时兜底）
```

- expired = 防泄漏兜底：注入后任务死亡无人回收时，平台强制失效。TTL 与任务时限
  **同钟**（累计 running，不含睡眠）——墙钟会让睡眠期间凭证悄悄过期，wake 后撞上
  失效的手。凭证是次要优先级（赋能第一）：此机制 P1 落地，P0 只留状态常量

## 4. 控制面工具清单（MCP）

**P0（17 个工具 / 5 个族，全部有真实消费者；description 直接写全使用说明——工具面
已薄到无需渐进发现，env 元工具出局 AD-023。machine 注册走 sandbox.yaml 配置声明
（零专用工具，Python 版同构）；machine_connect/disconnect 为 P1 条件触发项，AD-026）**：

**container 族（4）**——容器的一生（**docker backend 专属**，AD-027：ssh machine 上
调用返回 ErrUnsupported——capability 未声明；名字以被操作物命名，backend 是实现细节）：
container_create(name, {image?, tags?})            —— 造容器/同名自愈（见 §7.2）
container_remove(id?, {purge?=false})              —— 移除（默认保留 workdir，purge 才删）
container_build(tag, {dockerfile?})                —— 声明→标准镜像（见 §7.4）
container_images()                                 —— 可用镜像清单（create/build 的选型依据）

**shell 族（4）**：
shell_exec(cmd, {shell_id?, machine?, timeout?=60, async?=false})   —— 双模式执行（见 §4.2）
shell_new / shell_remove / shell_list              —— 会话管理（复用 cd/venv 状态用）

**file 族（4，探取型）**：
file_read / file_write / file_patch / file_search  —— 结构化文件操作（原子写 + 编码归一化 + 敏感路径警告）

**sense 族（3，只读观察：全景/回放/单查）**：
status()                                           —— 本体感觉快照（见 §4.3）
events(after_seq)                                  —— 事件流回放（见 §4.4）
resource_inspect(id?)                              —— 单资源深查（缺省 = 默认 env）

**memory 族（2）**：
state_get() / state_put(doc)                       —— 记忆文档（单文档无键，见 §4.1）

**P1**：machine_connect / machine_disconnect（**条件触发**——"任务中途接新机"场景
真实出现才建，默认答案 = 配置声明 + 热加载；machine_list 不设——status 全景已含
机器状态，AD-026）；伴生服务模式（替换 ssh 瘦模式）；lifecycle 睡眠动词
（env_sleep/env_wake——随配额执行器回归，docker backend 专属语义，AD-026 移除记录）；
commit 快照评估
**P2**：policy_set / policy_get / policy_versions（网络策略，随多租户治理）；
snapshot_*（workdir 轴，随多 backend）；credential_inject / credential_recycle
（无 read，见 §9）；metrics；vm/k8s backend

### 4.0 跟随式默认（走到哪，停到哪）

```
切换（驻留型）：container_create / shell_exec / shell_new（显式带 machine 参数时切 machine 默认）
不切（探取型）：file_* 全族、resource_inspect——拿一下东西不算换工作台
初始：首个 container_create；确认：status.default（唯一确认源）
省略 machine/env 参数 = 打默认
description 注明规则与漂移性（"默认 = 你最后一次驻留操作的目标，status 可确认"）
```

### 4.0.1 sudo/提权注记（v3.20，AD-020 的用户侧兑现）

ssh 场景提权不依赖 PTY 交互（Python 版从未解决 PTY 答 prompt，Windows UAC 根本不可
PTY 驱动）——shell_exec description 携带指引：

```
需要 stdin 的程序用管道预先喂：echo 'PW' | sudo -S apt install foo
多次提权先缓存凭证：echo 'PW' | sudo -S -v && sudo cmd1 && sudo cmd2
（sudo timestamp 默认 15min）管道来自 bash 内建，不进 ps 明文；密码属
Agent 自建凭证（userland，AD-013），平台审计不承诺脱敏
```

### 4.1 自状态文档（Agent 的记忆，AD-008）

**记录的是手的状态，不是 workdir 的状态**。workdir 是 Agent 随时切换、可多开的视角
（对人：打开一个文件夹就是一个 workdir；对 Agent 同理）——不是本体的锚点。状态文档
记录 Agent **握持的手 = 当前连接的机器/容器集合**（在此容器跑测试、在那台远程机
部署……）。

载体：**MCP 直连**（`state_get` / `state_put`），平台侧持久化（SQLite，**单文档**）。
不落 workdir——workdir 换了文档不换；wake 一发生即可读，无需等 workdir 重挂。

**单文档，无键（v3.10 终态）**：这个实例的记忆就这一份——记事本本来就是一本，不是
一堆散页。`state_get()` / `state_put(doc)` 无键无参，锚点 = 实例本身（零信息量，忘不了）。
（演进：v3.9 匿名 KV → v3.10 单文档。KV 的两个死穴——多 key 无真实需求（单机单用户
没有分文档场景，违反 AD-004；文档内自由分节即可）；key 遗忘 = 自指死锁（记忆的锚点
存在记忆里，忘 key 即失忆，`state_list` 也救不了——平台不解释键认不出主记忆）。
**锚点必须零信息量**：实例物理身份 > 客户端记的字符串。）

- **首次 = 空白纸即引导（v3.10）**：`state_get()` 在文档从未写过时返回空白模板
  （含一句"活体事实调 status"、勿存秘密警告）。**不携带 env 清单/网络位置等会腐烂的
  事实**（v3.5-3.7 曾计划播种全量身体事实，但平台承诺不改写文档 ⇒ 播种即过时；
  活体事实改由 status 实时供给，见 §4.3）
- **维护**：Agent 自己读、自己写——记任务上下文、哪些进程应该在跑、下一步计划；
  这是唤醒后自我定位的依据
- **边界：自叙记忆 ≠ 治理真相**。文档可以记错、可以过时；治理真相在 status / 审计
  事件流 / `env/inspect` 实时查询。平台不校验文档内容（否则它就成了第二个需要审计
  的对象）
- **秘密卫生（诚实条款）**：文档是明文存储且平台不校验——引导警告"勿存秘密"；
  凭证走注入/回收通道，不走记忆通道（次要优先级但一致性要保）
- **唤醒闭环 = 感觉 + 记忆对账（v3.8）**：wake → status（身体真况）+ state_get
  （我的记忆）→ 对账差异（记忆里该跑的进程 status 里是 dead？）→ 修正决策（重启）
  → 干活。如同人睡醒：先感觉身体，再回忆计划，不符则修正

### 4.3 status（本体感觉：一次调用看全身，v3.8 新增）

**只读聚合，零新状态**：从注册表/句柄表/策略引擎实时计算，无独立存储、无写路径。

返回：实例内全部 env 清单（各自生命周期/backend）、进程句柄 alive/dead、可用 capability、
网络位置（内网|外网）、凭证在场元数据（非值）、**default**（当前默认 env，跟随式默认
的唯一确认源，§4.0）、**machines**（已声明机器清单及连接状态
connected/disconnected/reconnecting——machine_list 不设专门工具，status 全景已含，
v3.21 AD-026）。

- 与 §4.1 分工：**status = 本体感觉（永远真，平台算）**，**state doc = 记忆（可过时，
  Agent 写）**——status 是 state doc 的活体校准源
- 与 `resource_inspect` 关系：inspect = 单资源深查；status = 实例全景浅查。
  Agent 日常先 status 后按需 inspect，一次调用免多次往返
- 作用域：P0 单用户单实例 = 全实例即全景；P2 治理身份（§11 三层 Subject）落地时
  按 Subject 过滤——治理元数据不等于会话管理，届时也不引入 session 概念
- **唤醒降级链不失忆**：容器丢失走重建时，状态文档在平台侧 SQLite 键值存续——
  身体可以换，记忆不丢（比 v3.5 的 workdir 方案更早可用：先有感觉，再有身体）

### 4.2 shell_exec 双模式（v3.19 终态）

**阻塞模式（async=false，默认）**：发命令、等到底、拿结果。
- shell_id 有 = 会话内跑（复用 cd/venv）；无 = 一次性 shell
- timeout 默认 60s；到点 = 杀进程组 → 返回 `{exit_reason:"timeout", 部分输出}`
  （确定性事实，不挂死；想跑更久 = async，语义自洽）
- 返回 `{exit_code, output(合并流，上限截断留尾), duration}`
- 输出**在返回值里**（同步）；无转 waiting 态、无轮询、无缓冲——阻塞执行就该笨

**异步模式（async=true）**：立即返回，进程后台跑，活过 shell 会话。
- **拒绝 shell_id**（错误提示）——async 一律新 bash，命令须自足
  （要环境就写进命令行：`cd /app && source .venv/bin/activate && exec npm start > server.log 2>&1`）
- 实现（linux）：`bash -c "exec " + cmd` + `SysProcAttr{Setsid: true}`——bash exec
  变身使 `Process.Pid` 精确 = 命令自身 pid；setsid 使其自成进程组
- 死活判定 = **内核收尸**（`cmd.Wait()` goroutine，非轮询探活——pid 复用风险为零，
  退出瞬间事件递送）→ 死亡事件进审计流 → 捎带送达
- 返回 `{handle_id, pgid, state:"running"}`；输出 Agent 自管重定向（异步输出落文件）
- 杀：Agent `shell_exec("kill -- -<pgid>")`（userland 命令，平台不造 signal 工具）

**句柄账本（数据，非工具）**：`handle → {pgid, cmd, state, exit_reason, env_id}`
持久化 SQLite（控制面重启不丢）；容器停止 → 全组标 dead（不静默清理）；
捎带与 status 只读消费。~~process_list/logs/signal~~ 已砍（AD-020）：观察干预归
shell（tail/ps/kill），"我起了什么"归 state doc。

**OS 分叉的内部化**：进程组 kill（linux pgid / windows taskkill /T）、setsid 语义、
shell 方言差异——全部是**伴生服务的内部实现分支**，协议层统一暴露
`exec/kill(handle)/wait` 语义，OS 无关（P1 伴生兑现；P0 ssh 瘦模式见 §10.0 分级）。

### 4.4 身体事件流——拉模式订阅，平台不向 Agent 脑内推消息（v3.15，AD-017）

**问题**：MCP 工具面原为纯拉（轮询 status），Agent 对"进程死了/env 崩了/
断线了"的感知延迟 = 轮询间隔，长任务需自起轮询循环。MCP 协议虽有 server → client
notification 能力，但**客户端支持度参差**（推了可能无人收），且推送的接收方是 Agent
的 context window——推 10 条通知 = 往上下文塞 10 条消息，打扰权应留在 Agent 手里。

**决策：拉模式订阅（events 增量游标 + 长操作阻塞等待），零协议依赖**：

| 机制 | 语义 | 已验证先例 |
|---|---|---|
| `events(after_seq)` | audit 流窄化视图（身体相关事件：进程死亡/env 状态迁移/断线重连），`after_seq` 增量拉取 = 游标订阅，一次调用拿到"自上次以来发生了什么" | audit_query（已设计）+ sandbox-mcp `audit_query`（已上线） |

- **不新增事件源**：events 读的就是 §6 审计流（P0 四类事件天然覆盖），只是给 Agent
  一个窄化的增量读通道——审计是治理真相，events 是身体的感官回放
- **与 MCP 原生 notification 的关系**：不使用。MCP resources/subscribe 生态未成熟，
  推送打扰不可控；若未来客户端生态普遍支持且出现真实需求，再评估（与 §12 WebSocket
  同一批）
- **与"心智归 Agent"一致**：平台不往 Agent 脑内塞消息；Agent 决定何时看，一看即得
  （感知权 = 拉模式；心智主权 = 不被打扰）

**description 内嵌拉取指引（v3.16）**——四家客户端源码实证（opencode/dsh/hermes/
Claude Desktop）后确认：唯一被普遍消费的通知通道是 `tools/list_changed`，其余通知
类型无处理器/stub/静默丢弃。与其适配各运行时的通知路径，不如利用 MCP 协议的
**100% 可达面**：工具 description 必然注入模型上下文——把拉取习惯写进状态敏感工具
的 description，等于把"订阅"编码进 Agent 的本能，零基础设施、全客户端一致。

- **只写状态敏感工具**（不烧不必要的 token）：`shell_*` / `file_*`（machine 可能
  断线）、`state_get`（唤醒后第一个调用，配对 events）
- **description 模板**（一行，中英随实现）：`Before operating, check events(after_seq)
  for body changes since your last check (process death, disconnection, state
  transitions).`
- **与错误内嵌指引互补**：description 负责"事前习惯"（主动拉），错误 hint 负责
  "事中发现"（v3.14 L3 立即失败错误已内嵌 reconnect 指引）——两面合成完整感知路径
- **模型依从性诚实条款**：description 指引不保证 100% 依从（instruction following
  中等敏感）——它提高拉取概率但不担保；可靠性兜底仍是 L3 立即失败 + wait 阻塞语义，
  指引是增强不是依赖
- **token 成本边界**：只在 4 类状态敏感工具加一行（约 +40 tokens/工具面），不全量
  加（11 工具全加 = 行为与上下文双重浪费）

**响应捎带通知（piggyback，v3.17）——感知的终态**：Agent 调用任何 MCP 工具时，
服务端在**响应里附带**滞留的身体事件（自上次投递以来的进程死亡/状态迁移/断线重连）。
拉取动作被吸收进既有调用——Agent 零新增成本、零协议依赖、全客户端一致（工具响应
是第二个 100% 可达面，第一个是 description）。

| 通道 | 语义 | 游标 |
|---|---|---|
| 响应捎带（自动） | 每次工具响应附带 pending events（无则省略字段 = 零开销） | 平台内维护**投递水位**（delivery watermark），Agent 无需传参 |
| `events(after_seq)`（手动） | 显式拉取，可回看任意时点 | Agent 自管游标 |

- **水位是连接层状态，不是会话建模**：per-connection 的投递水位由传输事实维护
  （连接 = 传输事实，非心智概念，不违反 v3.9 边界）；P0 单客户端无并发问题
- **payload 防淹没**：积压事件 > N 条时压缩为摘要行 + 提示"events(after_seq) 看详情"
  ——单次响应不被事件流淹没，context window 不被撑爆
- **description 指引降级为辅助**（v3.16 → v3.17）：捎带保证"必然送达"，指引只服务
  "主动回看历史"场景——从"必须养成的习惯"降为"可选的深度查询入口"，description
  文案相应改写
- **打扰权不变**：仍是拉模式变体——拉取时机由 Agent 的调用节奏决定（它选择何时
  调工具），平台只是把"响应"这个必然存在的事实变厚了一点；Agent 不看就不占额外
  轮次，看了就即时知晓
- **三通道合成的感知栈**：捎带（必然、即时、零成本）→ wait 阻塞（长操作等待中）→
  events 回看（任意时点）→ description 指引（主动习惯）。可靠性不再依赖任何单一
  通道的依从性

## 5. 权限与策略模型

```
策略 = 主体 × 资源 × 动作 × 条件
主体: tenant → task → agent session（三层）
默认放行 + 显式收紧（recovery-first 兼容）；default deny 仅用于 credential_read 结构性禁用
```

## 6. 审计子系统

事件模型：{id, ts, tenant, subject, resource_id, action, outcome, duration_ms, detail(脱敏)}
类型（按引入阶段分组，勿预建无消费者的事件类型）：
- P0：tool_call / resource_created / **resource_recreated**（自愈语义标记）/
  resource_removed / error
- P1：snapshot_created / snapshot_restored / policy_change / policy_blocked
- P2：network_decision / credential_injected / credential_recycled（file_op 归入
  tool_call 的 detail，不单列——文件操作经 shell/file 工具即 tool_call）
脱敏：秘密值存 sha256 指纹，明文永不落库
用途：Agent 复盘（自省决策链）、任务失败定位、安全追溯

## 7. 快照子系统

### 7.0 两轴恢复模型（env / workdir 正交）

exec_env 的可恢复状态拆为两个**正交轴**，各有独立快照流与保留策略：

```
exec_env
├── env 态（镜像层 + 容器层/VM 磁盘） → env 恢复：wake 续用（日常）+ 标准镜像重建（兜底）
└── workdir（/workspace 挂载卷）      → workdir 快照（文件级，去重存储）
```

**env 恢复策略（v3.7 简化）——声明式真相，命令式快照暂缓**：

| 恢复通道 | 机制 | 时机 | 代价 |
|---|---|---|---|
| wake 续用（日常） | start 复用容器可写层（非重建，进程不复活） | 环境没坏，只是睡醒 | 最便宜，文件系统原样 |
| 标准镜像重建（兜底） | 从 Dockerfile 构建的标准镜像 `run` | 环境坏了 / 容器丢失 | 快（镜像缓存）、可复现、可审计 |
| commit 快照（暂缓） | 先 stop 再 commit → run from tag | P1 评估：增量状态频繁且不值得 Docker 化时 | 慢、不可复现、独立快照流复杂度 |

- **增量状态的正向工作流 = 写进 Dockerfile**：Agent 装了镜像之外的依赖且需要保留，
  修正声明（Dockerfile）而非快照运行时——环境的**声明式真相**在 Dockerfile，
  与"执行环境不可变"哲学一致
- **时间宽容**：恢复的正确性优先于速度，NFR-1 的自愈重建延迟只约束"标准镜像重建"
  路径（见 §15）

**workdir 不变量（v3.19 升格，自愈的机制基础）**：

```
workdir = f(work_home, env_name)      —— 名字即地址
```

- 宿主机路径由 env 名派生（`work_home/<name>/` → 容器 `/workspace`，唯一 bind-mount）
- **同名 create = 同一工作现场**：容器实例可生可灭，目录随名字永续——恢复不是动作，
  是命名系统的副作用
- remove 默认保留目录（purge 才删）——名字是资产的地址，不是实例的属性
- **三角色同 inode**：Agent（容器内 /workspace）、sandboxd、daemon（宿主机
  work_home/<name>）各用自己的名字指同一份文件——容器路径⇄宿主机路径的双向翻译
  仅对 bind-mount 成立（overlay 无稳定翻译）——**workdir 是容器世界与宿主机世界
  之间唯一的确定桥**，container_build 的路径校验（§7.4）由此而来
- 跨节点约束（P2）：docker volume 本机绑定；多节点时 workdir 后端换网络卷，P0 单机无此问题

两轴恢复矩阵（v3.19 语义更新）：

| 故障模式 | 恢复动作 | 机制 |
|---|---|---|
| 环境坏、数据好（最常见） | remove + 同名 create（自愈），workdir 不变量自动保留 | 标准镜像 + 不变量 |
| 数据坏、环境好 | workdir 快照恢复（P1）/ git 检出 | workdir 快照轴 |
| 都坏 | 自愈 + workdir 快照恢复 | 上两者组合 |

**恢复边界 = 本地态**（哲学条款）：恢复只覆盖本地状态（容器层/文件系统/工作目录）。
对外部世界的副作用——服务端状态（会话/账号/风控）、支付、git push——结构性不可恢复，
其可观测性与管控由审计（network_decision / audit_query）与网络策略承担。
docker 沙箱同理（外部交互占比小而已）。

### 7.1 模型（P1——随 workdir 快照轴落地，P0 无快照存储）

> v3.13 标注：env commit 快照已裁（AD-009）、baseline = 镜像引用（零存储）、
> workdir 快照轴 P1——P0 没有任何快照存储，本节是 P1 的设计留档。

{id, resource_id, name, created_at, type: manual|auto, scope: workdir, size_bytes, backend_specific, status}

- ~~scope=full / full_pair_id~~（降 P1+ 评估：env 轴快照不存在后"成对打"结构上不可能，
  仅当 env commit 快照 P1 回归且出现真实需求时重启——勿增实体）
- ~~type: baseline~~（baseline := 标准镜像引用，不是快照记录，见 §7.3）
- ~~excluded: credential_ids~~（凭证 P2；workdir 永不在 env 快照内的约束保留）

### 7.2 自愈 = 同名重建（v3.19，revert 工具出局 AD-021）

**revert 之死**：其全部实质承诺已由两个更基础的结构兑现——
① **workdir 不变量**（§7.0）：`workdir = f(work_home, env_name)`，名字即地址，
重建自动回到工作现场；② **注册表 name 键控**：同名 create = 同一身份复活（审计连续）。
剩余差异仅为审计事件类型（`resource_recreated`），不值一个独立工具 + 五步流程 + 补偿链章节。

**自愈流程（Agent 视角两步）**：

```
1. resource_remove("dev-2")        （或它已处于 error 态）
2. container_create(name="dev-2", image=<版本>)   —— 同名！
   → workdir 自动 = work_home/dev-2/   （不变量保证，目录还在原地）
   → 身份延续（键控）、审计标记 resource_recreated（自愈语义）
```

一句话给 Agent：**拆了重搭，东西还在原地；换镜像版本 = 拆了重搭时给新镜像**。

**实现内部（平台侧，Agent 不可见）**：create 同名命中已存在记录 → 复活路径
（跳过重复注册、审计标 recreated、锁临界区覆盖 kill-旧容器 + run-新容器）。

**失败处理（原补偿链的归宿）**：重建失败重试一次 → 仍失败 → 环境标 error，
保留 workdir（不变量下结构上不可能被销毁）供人工排查。error 态锁保持到显式重建。

### 7.3 策略（按可重建成本分层保留）

| scope | 存储 | 保留 | 触发 |
|---|---|---|---|
| env | 标准镜像（Dockerfile） | 标准镜像由 Dockerfile 版本管 | 手动 snapshot 调用（env 轴快照暂缓，见下） |
| workdir | 文件级去重（chunk hash） | N=20 轮转（便宜可高频） | 手动 + git 仓库可选 auto-stash（P1 优化） |
| ~~full~~ | ~~env+workdir 成对~~ | — | ~~已砍（v3.13）——env 轴快照不存在后"成对"结构上不可能~~ |

**baseline := 标准镜像引用（v3.7 简化）**：标准镜像优先后，创建时 env == 标准镜像，
自动 baseline 的 commit 快照是零信息量复制——baseline 直接定义为标准镜像引用
（零拷贝零成本），自愈兜底 = 从标准镜像重建。
**commit 快照暂缓（v3.7 裁剪）**：引入独立快照流（stop/commit/tag/轮转）复杂度不匹配
当下收益——P0 的 env 轴恢复 = wake 续用 + 同名重建两档；Agent 在 env 内装了
镜像之外的增量依赖且需要保留时，正向工作流是**写进 Dockerfile → container_build（§7.4）**
（声明式真相），同名重建自然携带。commit 快照留作 P1 评估项。

### 7.4 container_build——声明式工作流的执行手（v3.19 新增）

**存在理由**：AD-007 说"增量状态的正向工作流 = 写进 Dockerfile"，但没有
"Dockerfile → 镜像"的工具，链路断一环——container_build 补上，标准镜像 =
container_build 产物，§7.3"标准镜像由 Dockerfile 版本管"由此有了执行机制。

```
container_build(tag, {dockerfile?="Dockerfile"})
  路径解析：dockerfile 相对默认 env 的 workdir
  约束：解析结果 ⊆ workdir —— 物理事实（daemon 只见 bind-mount，
        overlay 无稳定路径翻译），非任意限制
  拒绝 inline content —— Dockerfile 必须是 workdir 里的文件（可审计）
  产物：该 env 的新标准镜像 → 下次同名重建的新基线
  错误：bad_path / dockerfile_missing / build_failed（附 daemon 输出尾部）
```

**声明式自愈闭环**：`file_write(Dockerfile) → container_build(tag) → remove + 同名
create(image=tag)`——改声明、造基线、拆重搭，三步换新身体不丢工作。

**实现注记（容器化部署）**：Agent 给容器路径（/workspace/...），平台换算宿主路径
（work_home/<name>/...）喂 daemon——换算函数仅对 bind-mount 成立（§7.0 不变量）；
sandboxd 自身容器化部署时依赖 work_home 双路径（§13）。

## 8. 配置子系统（P1 起逐步引入）

网络策略（allow_domains/allow_cidr/deny_patterns，默认 deny 内网）——P1
命令策略（默认不拦任何命令——能力释放）——P0 结构存在（默认空），规则面 P1
限额定义（cpu/memory/pids/带宽——保活语义）——P1（随配额执行）
时限（任务最大时长）——P1（与凭证 expired 同钟机制）
全部走版本化恢复通道

## 9. 凭证子系统（P2 预留，P0 无消费者）

> v3.12 降级：逐层拆解后 P0/P1 无真实消费者——快照隔离的触发条件是**快照出境**
> （导出/跨机迁移/平台流转，P2）；无 read 的消费者是不信任 Agent 的多租户（P2）；
> recycle/expired 的消费者是长期任务轮换（P1尾~P2）。资源类型与状态常量保留
> （§2.2/§3.3），本节语义全部留档，代码随第一个真实消费者引入（AD-004）。

inject：读取 secret 源 → 注入环境 → 记录元数据（值脱敏）
recycle：清除环境变量 → 撤销 token → 状态 recycled
快照隔离（出境检疫）：快照离开平台边界（导出/迁移）前 detach 凭证，恢复后 re-inject
——本地快照不出境，无需检疫
边界：credential_read 结构上不存在

**Agent 自建凭证（v3.11 澄清）**：Agent 在环境内自己获取/创建的凭证
（`gh auth login` 的 token、自生成 ssh key）是 **userland 数据**——平台未经手
即不归平台通道管，结构性保证（无 read/不落平台快照）只覆盖平台注入物。存放按
生命周期意图自选：

| 意图 | 位置 | 语义 |
|---|---|---|
| 仅本次运行期 | env 层（容器文件系统） | 回滚即逝——本来就要临时 |
| 跨 env 重建存活 | workdir（资产轴） | 同名重建不丢（不变量），与代码同为资产 |
| 记住存在（非值） | 记忆文档记元数据（路径/用途/过期） | 秘密卫生条款覆盖 |

上交通道（P2 评估）：credential 资源已有 C/D（§2.2），可定义
`credential_register(value)` 让 Agent 把自建凭证上交平台通道，换取 recycle/
防泄漏兜底——语义预留不预建（AD-004）。平台不扫描 userland 内容来拦截自建
凭证（内容治理越界）；NFR-5 的承诺范围相应收窄为平台通道（见 §15）。

**MCP 入口认证（≠ 凭证子系统）**：凭证子系统是"Agent 用钥匙开环境的锁"，
入口认证是"谁的客户端能连上 sandboxd"——门锁不属于钥匙柜。P0 单机：unix socket
本机监听 = 天然门禁（无需 token）；HTTP 监听时要求 static token（sandbox.yaml
配置）。多租户认证是 P2 治理身份（§11）的范畴。

## 10. 数据面（Backend 契约）

**backend × OS 双轴矩阵（v3.19）**——backend = 传输/托管方式，OS = 目标系统，正交：

| | Linux | Windows | Android |
|---|---|---|---|
| **docker** | ✅ P0（本机容器） | ⚠️ P1 评估（Windows 容器/Docker Desktop） | ❌ 无原生 Docker |
| **ssh**（瘦模式过渡） | ✅ P0 | ✅ P0（最小过渡，见分级） | ⚠️ Termux 能跑非主流 |
| **companion**（伴生，终态） | ✅ P1 | ✅ P1（Go 跨编译，OS 差异内部化） | ✅ P1（**伴生架构的决定性用例**——瘦客户端在安卓不可用：无原生 Docker、shell 非标配） |
| vm/k8s | — | — | — P2 评估 |

```python
class Backend(ABC):
    capabilities: set[str]
    create/start/stop/remove
    exec(cmd, async?) / read_file / write_file
    kill(handle) / wait(handle)                      # OS 差异内部化（v3.19）
    snapshot(scope) / restore(ref)                   # P1（workdir 轴）
    metrics()                                        # P2
    health()                                         # 连接健康（v3.14）
```

### 10.0 远程伴生服务（remote companion，v3.14 架构第一公民）

**背景**：sandbox-mcp（Python 前身）的远程模式 = 瘦客户端——SSH 隧道透传 PTY，
所有健壮性逻辑（Windows 代码页、GBK 编码、PowerShell 管道模式无 Ctrl-C）都要本地端
逐平台适配，**异构在客户端这一端，永远追不完**。

**决策（vscode Remote-SSH 语义）**：连接远程时部署**伴生服务**——Go 静态自包含
二进制（一次编译，Linux/Windows/Android 直接运行），之后本地 sandboxd 与远端伴生服务做
**协议对话**而非隧道透传。健壮性由两端**同构程序**共同保证：编码/信号/PTY/进程
语义在协议层对齐，异构被伴生进程封装。

- **Go 静态二进制是采纳前提**：伴生服务必须"一次编译、scp 过去直接跑"——解释型
  语言（Python 需远端装解释器/依赖）不可行，这是本项目选择 Go 的架构级理由
- **会话与连接解耦（tmux 语义）**：shell/进程会话挂在伴生服务下、不挂在 SSH 连接上
  ——断线 = 遥控器没信号，远端会话存活；重连自动接管（见断线协议）
- **信任模型（默认半自动）**：伴生二进制 scp 至远端 + 打印启动命令，**人工确认执行**
  （部署常驻 daemon 信任门槛高）；全自动模式（vscode 式，连接即部署）为配置开关，
  默认关
- **功能继承声明**：PTY 持久会话、原子文件写、审计查询、敏感路径警告——继承
  sandbox-mcp v0.3 已验证设计，不重新发明

**瘦模式分级（P0 过渡，v3.19 修正）**——伴生未就绪时 ssh 是**最小过渡绳索**：

| 能力 | P0 瘦模式 | 说明 |
|---|---|---|
| connect + shell_exec 阻塞 | ✅ | 唯一核心承诺（Linux bash PTY / Windows PowerShell） |
| file_* | ✅ 完整 | 走 SFTP 子系统——字节流无编码问题 |
| **编码适配（代码页探测/GBK）** | ✅ **直接抄** | Python 版已有（encoding_utils + powershell_provider 语义）——代码债便宜就还 |
| **原子写 + UTF-8 LF no BOM 归一化 + 敏感路径警告** | ✅ **直接抄** | file_operations.py + safety.py 语义——纯函数孤立易移植，docker/ssh 两 backend 通用 |
| **sudo 提权** | ✅ 管道模式 | `echo PW \| sudo -S`（§4.0.1）——非交互，不依赖 PTY 答 prompt（Python 版也从未解决它） |
| **连接自愈（ControlMaster 语义 + 往返探测）** | ✅ 直接抄 + 加固 | Python 版 ControlMaster 三件套（见 §10.0.1）；Go 加固：往返探测防僵尸 master——比 Python 版的 `-O check` 多一步诚实 |
| async=true | ❌ 报错"unsupported, use companion (P1)" | 进程账本/收尸是架构缺口（无伴生无内核收尸），非代码量问题 |
| container_build | ❌ P1 伴生 | 需 daemon 可见宿主路径 + 构建流式回传——瘦模式只有 shell 透传，等终态 |
| 进程账本/断线五层 | ❌ | 架构债，必须等终态 |

**搬运原则：代码债便宜就还（孤立函数，伴生来了直接删）；架构债必须等终态**
（长在会话/账本/协议的骨头里，重造 = 重新踩坑）。

### 10.0.1 ssh 连接自愈——ControlMaster 三件套 + 僵尸探测（v3.22，AD-028）

P0 瘦模式的连接层自愈（继承 Python 版已验证设计 + 一处关键加固）：

**三件套（Python 版 `_ensure_alive`/ControlMaster 语义）**：
1. **连接复用**：`ssh -M -S <socket> -o ControlPersist=300`——master 后台保活 5 分钟，
   后续操作多路复用，不重复握手（瞬时网络抖动无感）
2. **操作前探活**：每次操作前检查 master 死活；死 → 用声明配置透明重建——调用方
   不见 stale 连接；**机器注册永不摘除**（重连失败 = disconnected 状态，status 可见）
3. **显式重连**：status 提示 Agent 可触发（machine 层的 wake 投影）

**加固：往返探测替代 `-O check`（AD-028 核心）**——`-O check` 只证 socket 文件活着，
不证 master 可用。僵尸 master（网络半开/sshd 半死/Windows conhost 会话泄漏把
server 侧拖坏）的症状：check 通过但**新 channel 打开挂死**——Python 版对此无解。

```
每次操作前：走 master 跑 echo <nonce>，5s 超时
  ├─ 通 → 健康，执行操作
  ├─ 挂/超时 → 僵尸 master：-O exit 强拆 + socket 清理 + 全新 master 重建
  │            （重建仍失败 → machine 标 disconnected，操作报明确错误）
  └─ 连接不存在 → 正常惰性重建
```

**会话卫生（Windows 经验，Python 版 powershell_provider 已验证）**：
- PowerShell 会话用 `-File -` stdin 脚本模式（无 PTY 干扰）+ `-NoLogo -NoProfile`
- **console 状态隔离是 ssh 协议原生的**：每 session channel 独立 conhost/PTY 实例，
  一个 PowerShell 会话改 OutputEncoding/chcp 不污染其他 channel——多路复用 socket
  只传字节不带 console 状态
- **per-machine 并发会话上限（默认 4）**：防 Windows OpenSSH conhost 清理 bug 的
  进程树堆积（server 侧病了症状像"socket 污染"——新会话全挂），也防 Agent 失忆开满屏
- **每操作硬超时**：channel 打开挂死不拖死工具调用

**边界（诚实条款）**：这是连接层自愈，不是断线五层——无主动心跳（L1）、无会话
解耦（L4：SSH 死时远端 shell 进程仍全灭）、重连是惰性的（下次操作时才发生）。
瘦模式自愈到"瞬时抖动透明 + 僵尸可自清 + 永久断线可见"为止，五层等伴生。

**断线协议（五层，v3.14）**——瘦模式的结构性缺陷（SSH 死 = 一切死，Agent 只能用
试错发现故障）在伴生架构下获得完整解法：

| 层 | 机制 | 瘦模式能否做到 |
|---|---|---|
| L1 探测 | sandboxd 对伴生服务心跳 ping（间隔 5s，3 次失败判断线） | ❌ 只能被动等操作失败 |
| L2 表达 | machine 状态：`connected / disconnected / reconnecting`，status 一眼看穿 | ❌ 无连接状态概念 |
| L3 反馈 | disconnected 时 exec/写文件**立即失败**（明确错误："machine disconnected, awaiting reconnect"），不挂超时 | ⚠️ 只能做本层 |
| L4 解耦 | 会话存活于伴生服务，断线不杀会话（远端进程/PTY 原样） | ❌ SSH 死即 SIGHUP 全灭 |
| L5 自愈 | sandboxd 自动重连握手，成功后会话原样找回，Agent 无感 | ❌ |

Agent 体验：从"不断尝试开新 powershell 试错"变为"status 瞥一眼 → disconnected →
等自愈或按错误指引行动"。

| Backend | capabilities | snapshot 实现 | 隔离级别 | 适用 |
|---|---|---|---|---|
| docker | shell/files/snapshot | wake 续用 + 标准镜像重建（commit 快照 P1 评估） | 容器（共享内核） | 默认：开发/执行，秒级启停 |
| ssh | shell/files（伴生模式，见 §10.0） | 无（诚实声明） | 无（复用既有主机） | 接入既有物理机/远程开发机；瘦模式仅作 bootstrap |
| vm (future) | shell/files/snapshot | libvirt/qemu 外部快照（VM 级 CoW）或存储层快照（qcow2 内部快照/切片） | 硬件级虚拟化（独立内核） | 需要内核隔离/不同 OS/系统级实验（内核模块、网络栈、多发行版矩阵） |
| k8s (future) | shell/files/snapshot | Pod 重建 + PVC 快照 | 容器编排（多节点） | 平台托管形态：多节点调度、PVC 原生两轴快照 |

**vm 与 docker 的选型语义**：默认 docker（快、轻、秒级自愈重建）；当任务需要**独立内核**
（内核模块/驱动实验、安全研究、跨 OS 矩阵——Ubuntu/macOS-Windows via VM）时 vm backend
提供硬件级隔离。快照语义对齐两轴模型：VM 磁盘镜像外部快照（CoW 层）= env 轴，
挂载的共享目录/云盘 = workdir 轴。启动秒级→分钟级的代价由隔离收益换。

**capability = 平台保证的原语，不是软件清单（v3.2 修订）**：
- 沙箱 = 开发/执行环境的通用底座，**对用例中立**：爬虫、浏览器、数据库都是环境里
  跑的程序（userland），不是平台概念，设计语言不为其开口子
- capability 只声明**运行时本身提供**的能力（shell/files/snapshot）；
  应用软件是用户镜像内的自由——平台不承诺、不预装、不背书
- 把任何应用软件声明为 capability 是空头承诺：自定义镜像无法保证其存在，capability 即谎报
- 执行环境不可变哲学的一致推论：环境内容 = 镜像（用户选） + 运行时保证（平台声明），
  两者边界不容缝合
- Capability 为开放字符串类型：未来专用 backend（承诺不同运行时形态）可自定义声明

错误模型：{code, message, retryable, detail} + 统一超时

## 11. 多 Agent / 多任务 / 多租户

三层身份：tenant（边界）→ task（资源归属）→ agent session（操作主体）
配额：tenant 级总量 + task 级单任务上限（保活）
隔离：网络（per-task 命名空间）、文件（容器天然）、凭证（per-task 作用域）
审计：事件带 task/tenant 标签

## 12. 可观测

审计事件流 + metrics（docker stats 时间序列，P2）。WebSocket 遥测推送降为评估项
（P0/P1 Agent 轮询 status 已覆盖状态感知；推送仅当出现"事件延迟敏感"的真实场景——
勿增实体）。

计量预留：核心只负责发审计/指标事件流；计量与计费是事件流的下游消费者，不属于开源核心
（接缝设计见 §14.1，代码随平台层动工引入）。计量单位与定价属商业策略，见内部文档。


## 13. 部署形态

单进程 MCP server（stdio/unix socket/HTTP）+ sandbox.yaml 配置 + SQLite（审计/注册表/记忆/句柄）+ 文件系统（快照/工作目录存储）

入口认证（门锁，见 §9）：stdio/unix socket = 本机天然门禁；HTTP = static token（sandbox.yaml）。多租户认证 P2。

### 13.1 work_home 双路径（容器化部署硬约束，v3.19）

sandboxd 自身可能跑在容器里（经 socket/HTTP 对接 daemon）——此时"宿主机路径"
对 sandboxd 不可见，但 container_build 需要 daemon 可读的宿主路径。解法 = 显式双路径：

```yaml
storage:
  work_home: /work_home                    # sandboxd 视角（volume 挂载点）
docker:
  work_home_host: /srv/agent-sandbox/work_home   # 宿主机视角（daemon 用）
  network: "agent-sandbox"                 # env 互联：容器名 DNS（见下）
```

- 不变量全式：`env_name → sandboxd 路径 storage.work_home/<name>`（文件操作用），
  `env_name → 宿主路径 docker.work_home_host/<name>`（daemon build 用）——
  两路径同一 inode（部署时 volume 绑定保证）
- **启动 fail-fast 校验**：检测到容器内部署 + work_home_host 缺失 → 拒绝启动
  （配置错误拦在所有工作之前）；可选自检：inspect 自身 Mounts 与声明比对
- 裸机部署：两路径合一，配置无感

### 13.2 DNS 网络（env 互联，v3.19——继承 sandbox-mcp auto_network）

- 管理的全部 env 加入 user-defined bridge 网络（首次 create 时 daemon 侧自动建）
- **容器名即主机名**：Agent 在 dev-2 里 `curl http://build-1:8000` 直接通，零配置发现
- **不开宿主端口映射**（继承 Python 版立场：暴露宿主端口会破坏沙箱网络隔离）——
  env 间访问走容器网络；port_expose 接缝（P1）另有评估
- 文件跨 env 传输：小文件走 file_*（指明目标 env 路径），大文件走 DNS 网络 +
  userland 传输（`http.server` + `curl`）——**不设共享目录机制**（backend 专属 +
  shell 可代 + 双路径税，AD-023 否决；admin god-mode 容器同批否决）

## 14. 开源核心与平台层边界

```
开源核心（本仓库，AGPL-3.0）
  资源模型 / 编排 / backend / 快照 / 策略 / 审计 / metrics
  单进程 + SQLite + sandbox.yaml；tenant 固定为 default，token 认证
        │  唯一接缝：计量事件流（audit + metrics）
        ▼
平台层（独立部署，商业形态）
  用户系统（注册/登录/tenant 供给）+ 用量账本 + 计费 + tenant 级配额执行
```

- 三层身份（tenant/task/session）的**模型**留在核心；身份的**供给**（注册、付费、租户开通）属于平台层
- 核心不感知计费：`tenant` 字段在开源形态为常量，平台形态由平台层填充——同一套代码两种形态
- AGPL-3.0 已保证托管 SaaS 场景的修改开源约束，与 open-core 策略一致

### 14.1 平台层接缝清单（spec 即预留）

> v3.3 修订：接缝设计只保留在 spec 层面，代码不预建零消费者抽象。
> 平台层动工时按本表引入对应代码接缝（依赖单向：平台 → 核心）。

| 接缝 | 设计位置 | 开源形态 | 平台形态 |
|---|---|---|---|
| 三层身份模型 | `resource.Subject`（贯穿 Meta.Owner / audit Subject 字段） | 常量 "default" 字符串 | 平台层引入 Subject 类型，填充真实 tenant/task/session |
| 事件流扇出 | `audit.Bus` 扩展 Subscriber | Sink 直连 | 平台层插入计量/计费 Subscriber |
| 计量载荷 | `audit.Event.Metering` 字段 | 不计量 | 用量计数 + 单位（exec_seconds/snapshot_bytes） |
| 租户配额执行 | `policy.TenantQuota` 接口 | 无配额执行 | 平台层实现扣费/限额 |
| 契约包可见性 | `pkg/`（已就位） | 同进程直连 | 平台层作为独立 module 导入 |

契约在 `pkg/`、装配在 `cmd/`、平台层未来经 Go module 跨仓库导入契约并注入实现——依赖单向（平台 → 核心），无循环。

---

## P0 施工清单（对照 spec 的实现顺序）

| 里程碑 | 内容 | 对应章节 |
|---|---|---|
| P0-m1 ✅ | 资源模型 + backend 契约 + 快照/策略/审计骨架（纯契约，零死代码） | §2 §10 |
| P0-m2 | SQLite 审计 sink + 注册表（name 键控）+ 状态机（含并发仲裁锁）+ 句柄账本 | §3 §6 §13 |
| P0-m3a | docker×linux backend：container_create(同名自愈)/shell_exec 双模式/file/container_build | §4 §7 §10 |
| P0-m3b | ssh×linux + ssh×windows（瘦模式最小过渡：配置声明注册/阻塞 exec/file_*（SFTP）/编码层抄） | §4 §10.0 |
| P0-m4 | sandbox.yaml 配置装配（双路径/DNS 网络）+ MCP server（7 工具） | §4 §13 |

P1：**伴生服务**（替换瘦模式——sandboxd-lite 二进制 + 断线五层 + async/账本在远程启用，
§10.0——P1 第一个硬仗；覆盖 linux/windows/android 三 OS）+ machine_connect/disconnect
（**条件触发**：仅当"任务中途接入陌生主机"场景真实出现；配置声明 + 热加载是默认答案；
machine_list 不设——status 全景已含机器状态，AD-026）+ lifecycle 睡眠动词
（env_sleep/env_wake，随配额执行器，AD-026）+ 审计脱敏 + workdir 快照轴 +
commit 快照评估 + docker×windows 容器评估
P2：三层身份 + policy_* + snapshot_* + credential + metrics + vm/k8s backend 评估

验证矩阵（`docs/verification/feature-test-matrix.md`）在 P0-m3 首个可运行 backend
落地后建立——0 代码阶段无矩阵可写，预造空转。测试层级约定沿用 U/UI/P/D/M
（P0 无 UI/设备，主要 U 契约测试 + P 管道级集成）。

---

## 15. 可测量 NFR（哲学 → metric 对照）

定性哲学翻译为可测量 NFR（没有 metric 的原则是口号，不是需求）：

| ID | 哲学（定性） | 可测量 NFR | 目标值 | 测量方式 |
|---|---|---|---|---|
| NFR-1 | 恢复优先 | 自愈重建延迟（同名 create 路径） | p95 < 5s（镜像缓存命中） | 重建流程计时 |
| NFR-2 | 恢复优先 | 自愈成功率 | > 99%（镜像兜底后） | 重建失败分支计数 |
| NFR-3 | 能力释放 | 命令默认放行率 | = 100%（命令策略默认空） | policy.Decision 采样 |
| NFR-4 | 执行环境不可变 | update 接口数 | = 0（改配置 = 重建） | API 面审计 |
| NFR-5 | 凭证最小暴露 | 平台通道快照中明文密钥数 | = 0（范围 = 平台注入物；Agent 自建 userland 凭证不在平台承诺内，见 §9） | 快照载荷脱敏校验 |
| NFR-6 | 凭证最小暴露 | credential_read 工具数 | = 0（结构性禁用） | 工具面审计 |
| NFR-7 | 审计 = 复盘 | 事件可回溯率 | = 100%（脱敏后） | audit Query 覆盖 |
| NFR-8 | 配额 = 保活 | 单任务超限不影响其他任务 | 无饥饿（P2 验证——配额执行器随治理身份） | 配额测试 |
| NFR-9 | 赋能操作 | env 冷启动延迟（create→running） | p95 < 10s（标准镜像有缓存） | create 流程计时 |
| NFR-10 | 赋能操作 | exec 往返延迟（同步 shell） | p95 < 500ms | exec 计时 |

P0 阶段只强制可测的（自愈/放行/不可变/冷启动/exec——凭证条随子系统 P2 落地）；
其余随 P1/P2 落地。

---

## 16. 决策记录（AD-NN）

> 架构记录结果，决策记录取舍。为什么不用 X 的答案必须在仓库里，不在记忆里。

### AD-001: recovery-first（恢复优先）而非拦截式沙箱

- 状态: Accepted
- 背景: 沙箱传统靠拦截命令（default deny）保证安全，但拦截削弱 Agent 能力，且拦截清单永远追不上工具演进
- 备选方案:
  - 拦截式（default deny + 白名单）: 拒绝 — 白名单维护成本随工具增长，且误伤合法操作
  - 纯隔离不恢复: 拒绝 — 隔离防外部，防不住 Agent 自己把环境搞坏
- 决策: 不拦任何命令（能力最大化），安全靠「环境基线 + 随时回滚」兜底
- 影响: 核心承诺是「赋能操作」（见 AD-006 第一性重定位）；策略模型默认放行 + 显式收紧；恢复机制是自愈支撑（见 AD-002）
- 日期: 2026-08-19

### AD-002: 两轴恢复模型（env / workdir 正交）

- 状态: Accepted
- 背景: 若 env 与 workdir 打同一个快照，revert 环境会把 Agent 的新工作成果一起回滚掉
- 备选方案:
  - 单轴快照（env+workdir 同快照）: 拒绝 — revert 即丢工作，「环境是牛、工作目录是资产」被违反
  - workdir 无快照、只靠 git: 拒绝 — 非代码内容（数据集/输出）无 git，且 git 不是平台责任
- 决策: env 与 workdir 两个正交恢复轴，各有独立快照流与保留策略；env revert 重挂原 workdir 卷
- 影响: 引入 scope: env|workdir|full；保留策略分层（env N=5 / workdir N=20 / full N=2）；恢复边界 = 本地态（外部副作用不可恢复，走审计）
- 日期: 2026-11-20

### AD-003: capability = 平台原语，非软件清单（含 browser 删除）

- 状态: Accepted
- 背景: 曾把 browser 列为独立 exec_env 类型，后降为 docker capability，最终删除
- 备选方案:
  - browser 独立 backend: 拒绝 — 浏览器状态一半在服务端（会话/风控），revert 无法复现外部状态，recovery-first 结构性不成立
  - browser 作为 docker capability: 拒绝 — chromium 是应用软件，自定义镜像无法保证其存在，声明即谎报
- 决策: capability 只声明运行时本身提供的原语（shell/files/snapshot）；应用软件是用户镜像内的自由，平台不承诺不背书
- 影响: CapBrowser 删除；设计语言对用例中立（爬虫/浏览器/数据库都是 userland 程序）；P2 减负
- 日期: 2026-11-20

### AD-004: 文档领先代码（spec documents, code follows demand）

- 状态: Accepted
- 背景: 早期把平台层接缝（Subject/TenantQuota/Metering）预建进代码，审计发现 42% 死代码
- 备选方案:
  - 代码预建接缝（接口先行）: 拒绝 — 零消费者抽象 = 死代码，接口缺实现反馈会漂移
  - 文档即预留（spec 记录设计、代码随需实现）: 采纳
- 决策: 接缝设计只保留在 spec（§14.1），平台层动工时按表引入代码接缝
- 影响: **文档与代码存在有意漂移** —— 状态机（spec 8 态 vs 代码 3 态）、事件类型（12 类 vs 2 类）、快照 scope（3 种 vs 仅 env）—— 是「spec 为完整设计、代码为 P0 最小实现」的有意偏差，非缺陷；由本节统一声明，不逐处另立 AD
- 日期: 2026-11-23

### AD-005: open-core 边界（开源核心 + 私有平台层）

- 状态: Accepted
- 背景: 平台化（计费/用户系统）与开源核心需切分，避免商业策略进公开仓库
- 备选方案:
  - 全开源（平台层也 AGPL）: 拒绝 — 竞品可自部署完整平台
  - 全私有（延迟开源）: 拒绝 — 失去公开定位与生态窗口，且核心工作本就可公开
- 决策: 开源核心（AGPL）+ 私有平台层（独立私有仓库）；接缝 = 计量事件流；依赖单向（平台 → 核心）
- 影响: pkg/ 契约可跨模块导入；docs/business/ 本地私有不入库；platform 分支只推私有 remote
- 日期: 2026-11-20

### AD-006: 第一性重定位——赋能操作（身体/手/支点）而非沙箱（隔离/防御）

- 状态: Accepted
- 背景: 原定位"沙箱平台"把 recovery-first 当核心哲学，陷入与 E2B/Daytona 的隔离红海；
  且"恢复优先"无法回答恶意 Agent 的安全命题（回滚恢复不了外部副作用），叙事自相矛盾
- 备选方案:
  - 沙箱框架（隔离优先 + 防御）: 拒绝 — 把 Agent 当威胁，削弱操作能力；E2B 红海，无差异化
  - 身体框架（赋能操作 + 自愈）: 采纳 — Agent 是主体非威胁；恢复/安全是支撑操作的"自愈"，非目的
- 决策: 第一性 = 赋能操作（给 AI 身体和手，作为 AI 操作现实的支点）；恢复与安全降为次优先级
  （防止自我崩溃，非防外敌）
- 影响: 核心哲学重排（赋能第一、自愈第二）；spec §0 重写；"不拦命令"获得更深根据
  （拦截 = 废手）；叙事从防御姿态转赋能姿态。与 AD-001 关系：AD-001 的"不拦命令"决策
  保留，但其理由从"安全靠回滚"升级为"赋能操作是第一性"
- 日期: 2026-11-23

### AD-007: env 恢复——标准镜像优先、commit 备选（先 stop 再 commit）

- 状态: Accepted → **Superseded（commit 部分，v3.7 由 AD-009 裁剪为暂缓）**
- 背景: 原 env 快照 = `docker commit / run from tag`，但 commit 运行中容器有大问题：
  崩溃不一致（半写状态）、慢（大容器几十秒）、镜像层膨胀，与 NFR-1（revert p95<5s）矛盾
- 备选方案:
  - commit 运行中容器: 拒绝 — 崩溃一致性问题 + 慢 + 膨胀
  - 纯 commit（先 stop）: 部分采纳 — 消除崩溃一致，但仍慢、不可复现
  - 标准镜像优先 + commit 兜底: 采纳 — 声明式真相在 Dockerfile，重建快/可复现/可审计；
    commit 只保运行时增量（Agent 临时装依赖），先 stop 再 commit，时间宽容
- 决策: env 恢复 = 标准镜像重建（首选）+ commit 快照（备选，先 stop 再 commit）；NFR-1
  只约束标准镜像路径，commit 路径时间宽容
- 影响: §7.0/§7.3/§10 更新；恢复的正确性优先于速度；与"执行环境不可变"哲学一致
  （声明式真相在 Dockerfile，commit 是运行时增量的逃生舱）
- 日期: 2026-11-23

### AD-008: 身体自治——心智归 Agent（进程/睡眠/自状态文档）

- 状态: Accepted
- 背景: 赋能操作（AD-006）推出后暴露三个旧框架缺口：无后台进程语义、无睡眠/唤醒
  续用通道、Agent 唤醒后无处自我定位。三者共同指向一个原则切面：平台越界替 Agent
  记忆与决策
- 备选方案:
  - 平台管进程守护/自动重启: 拒绝 — 平台不知道"该跑什么"（应用语义），复活进程 =
    重建应用状态，复杂易腐；越俎代庖
  - 平台维护状态文档: 拒绝 — 文档须承载 Agent 的主观任务上下文，平台只能写客观事实；
    平台维护则文档成治理对象，需要配套审计，成本倒挂
  - 身体归平台、心智归 Agent: 采纳 — 平台保证持久性与事实（stop 保留可写层、播种
  身体事实头部），Agent 决定进程、睡眠、记忆
- 决策: ① 进程手（exec_async/process_*，per-env 句柄，平台不复活进程）；② 自状态文档
  （MCP 直连 state_get/state_put，平台侧随 session 持久化；记**手的状态**——Agent 握持的
  机器/容器集合，非 workdir 状态；平台播种一次、永不改写，自叙记忆 ≠ 治理真相；
  v3.6 修订：原 workdir 载体方案作废——workdir 是可切换多开的视角，不是本体锚点）；
  ③ 睡眠/唤醒 Agent 自主（wake = start 续用，恢复阶梯最便宜档；失败自动降级重建）
- 影响: §3.1（sleep/wake + 降级链）、§4.1/§4.2 新增、NFR-9/10（赋能度量）；
  唤醒闭环 = wake → state_get → 重启进程 → 继续干活；策略时限默认仅计 running
- 日期: 2026-11-24

### AD-009: v3.7 一致性收口——session 键/快照裁剪/句柄持久化/凭证同钟

- 状态: Accepted
- 背景: v3.6 自审暴露四个一致性缺口：① 状态文档"随 session 持久化"但 session 生命周期
  未定义（session = 连接则重连失忆，文档失去存在意义）；② baseline commit 快照与标准
  镜像零信息量冗余；③ 进程句柄表内存态，控制面重启即丢；④ 凭证 expired TTL 未定钟
- 备选方案:
  - session 绑连接: 拒绝 — MCP 连接幂等无状态（协议层 initialize 不携带业务身份），
    绑连接 = 每日重开客户端即失忆
  - session 显式命名键: 采纳 — 客户端声明 session_id（默认 default），键跨连接稳定，
    持久化在键不在连接；文档锚 = 命名的工作身份
  - commit 快照引入: 暂缓 — 独立快照流（stop/commit/tag/轮转）复杂度不匹配当下收益；
    增量状态正向工作流 = 写进 Dockerfile（修正声明而非快照运行时）
  - 自动 baseline commit 快照: 删除 — 标准镜像优先后创建时 env == 镜像，baseline :=
    标准镜像引用（零拷贝），补偿链兜底 = 从镜像重建
- 决策: ① session_id 客户端声明、跨连接稳定、TTL GC（P1）；② env 轴恢复 = wake 续用 +
  标准镜像重建两档，commit 快照 P1 评估；baseline = 标准镜像引用；③ 进程句柄持久化
  SQLite（P0-m2），容器停止句柄标 dead 不静默清理；④ 凭证 expired TTL 与任务时限同钟
  （累计 running，不含睡眠；凭证次要优先级，P1 落地）
- 影响: §3.1/§3.3/§4.1/§4.2/§7.0/§7.3/§10/AD-007 更新；P0 快照子系统收窄为
  workdir 轴 + 镜像引用（更小施工面）
- 日期: 2026-11-24

### AD-010: status 工具——本体感觉与记忆分离（v3.8）

- 状态: Accepted
- 背景: 播种头携带身体事实（env 清单/网络位置）与"平台永不改写文档"承诺矛盾——
  播种即过时（env 删了崩了，文档还留着旧事实）；Agent 需要一个永远真的活体状态入口
- 备选方案:
  - 平台定期刷新播种头: 拒绝 — 违反"平台不改写文档"（AD-008 边界），文档半平台半
    Agent 所有，权责混乱
  - Agent 靠 inspect/audit 自行拼凑: 可行但贵 — 多次往返，且无全景视图
  - status 只读聚合: 采纳 — 一次调用看全身，零新状态（注册表/句柄表实时算）
- 决策: 新增 `status` 工具（只读聚合：env 清单+生命周期、进程 alive/dead、capability、
  网络位置、凭证在场元数据）；播种头收缩为最小引导（"活体事实调 status" +
  勿存秘密警告）
- 影响: §4.1 播种头收缩、§4.3 新增；唤醒闭环升级为感觉+记忆对账（wake → status +
  state_get → 对账 → 修正 → 干活）；status = 本体感觉（永远真），state doc = 记忆
  （可过时），前者是后者的活体校准源；与 AD-008 无冲突（status 非文档，不改写）
- 日期: 2026-11-24

### AD-011: 记忆键去 session 化——匿名键值，平台不建模会话（v3.9）

- 状态: Accepted（Supersedes AD-009 的 session 键决策①；键值设计后被 AD-012 终态化）
- 背景: session 是 Agent 会话管理的范畴（心智层）；平台（身体层）键控 session_id
  违反分层纪律（I7）与 AD-008 自己立的边界（"心智归 Agent"）——身体在替心智登记会话
- 备选方案:
  - session_id 键控（AD-009 ①）: 撤回 — 键语义（会话）平台必须解释 = 越界建模
  - 匿名键值: 采纳 — key 由客户端自带、平台不解释，只保证持久化/跨连接稳定/GC
- 决策: `state_get(key) / state_put(key, doc)` 匿名键值文档；未命中返回引导模板
  （替代"播种"——平台从此无任何写文档路径）；status 作用域 = P0 全实例，P2 按
  治理 Subject 过滤（治理元数据 ≠ 会话管理）
- 影响: §4.1/§4.3/工具清单更新；键语义整体活在客户端层；§11 三层身份不受影响
  （那是治理身份供给，非会话管理）
- 日期: 2026-11-24

### AD-012: 记忆终态 = 单文档无键（v3.10，Supersedes AD-011 的键值设计）

- 状态: Accepted
- 背景: AD-011 匿名 KV 仍有两个死穴：① 多 key 无真实需求——P0 单机单用户没有分文档
  场景（假想消费者，违反 AD-004），文档内自由分节更灵活；② key 遗忘 = 自指死锁——
  记忆的锚点存在记忆必须被记住才能找到，忘 key 即失忆；`state_list` 救不了（平台不
  解释键，认不出哪个是主记忆）
- 根因: **锚点不能由记忆自己持有**——必须零信息量（不需要记住的东西才不会忘）
- 备选方案:
  - 匿名 KV + state_list: 拒绝 — 见背景②，列表无语义仍是死锁
  - 单文档（实例物理身份为锚）: 采纳 — 这个 sandboxd 的记忆就这一份，忘不了
  - 未来多身份 per-Subject 文档: P2 — 锚由认证层供给（治理身份），仍非 Agent 记忆
- 决策: `state_get() / state_put(doc)` 无键无参；从未写过时返回空白模板（引导）；
  删除 = 简单覆盖/清空（客户端显式）
- 影响: §4.1/工具清单更新；记忆接口缩减为两个零参数工具——平台对 Agent 心智的
  假设降到零（不建模会话、不建模身份、不建模主题划分）
- 日期: 2026-11-24

### AD-013: Agent 自建凭证归 userland——平台保证自己经手的，不保证自己看不见的（v3.11）

- 状态: Accepted
- 背景: status/credential 结构性无 read 只覆盖平台注入物；Agent 在环境内自建的凭证
  （gh auth login、自生成 key）归属未定义——暴露 NFR-5 隐藏矛盾：workdir 快照捕获
  userland 文件，若 Agent 把 token 存 workdir，"快照明文密钥 = 0"必然被打破，除非
  平台扫描内容拦截（内容治理，越界）
- 备选方案:
  - 平台扫描快照内容拦截秘密: 拒绝 — 内容治理违反"平台不解释 userland"边界，
    且扫描永远有漏报，虚假安全感
  - 自建凭证禁止: 拒绝 — 无法执行也违背赋能第一性（Agent 操作现实必然积累凭证）
  - 归 userland + 意图自选存放 + NFR-5 收窄: 采纳 — 谁创造谁负责；平台通道保证
    平台注入物
- 决策: 自建凭证 = userland 数据；存放按生命周期意图自选（env 层临时 / workdir
  资产 / 记忆文档记元数据非值）；可选上交通道 credential_register P1 评估
  （语义预留不预建）；NFR-5 范围收窄为"平台通道快照"，诚实声明承诺边界
- 影响: §9 新增澄清节、NFR-5 措辞；平台保证边界从此对称：**保证自己经手的，
  不保证自己看不见的**——这是诚实条款而非安全放松（扫描的漏报才是虚假安全）
- 日期: 2026-11-24

### AD-014: 凭证子系统降级 P2 预留——P0 唯一认证是 MCP 入口门锁（v3.12）

- 状态: Accepted
- 背景: 连续追问拆完了凭证的存在理由：快照隔离的触发条件是快照**出境**（P2）；
  无 read 的消费者是多租户不信任场景（P2）；recycle/expired 是长期任务轮换（P1尾
  ~P2）；审计脱敏是审计的功能而非凭证的。P0/P1 全程无真实消费者。且需区分两类
  概念：凭证子系统 = Agent 用钥匙开环境的锁；MCP 入口认证 = 谁能连上 sandboxd
  的门锁——门锁不属于钥匙柜
- 备选方案:
  - 凭证子系统保留 P1: 拒绝 — 无消费者即施工（违反 AD-004）
  - 整节删除: 拒绝 — NFR-5/6 与 §3.3 状态机需要主体；语义留档成本低
  - 降级 P2 预留 + 入口认证独立: 采纳
- 决策: §9 降级为 P2 预留（资源类型/状态常量/本节语义全部留档，代码随第一个
  真实消费者引入）；credential_inject/recycle 工具标 P2；P0 入口认证 = unix
  socket 天然门禁（HTTP 监听时 static token）；detach/re-inject 语义修正触发
  条件为"快照出境检疫"（本地快照无需）
- 影响: §9/§4/§13/P1 清单/NFR 分期更新；P0 施工面进一步收窄（凭证子系统完全
  出局）；AD-013 的"平台通道"承诺顺延至 P2 兑现
- 日期: 2026-11-24

### AD-015: 奥卡姆清扫——工具面按阶段分组，P0 零快照存储（v3.13）

- 状态: Accepted
- 背景: 全文"谁消费它"拷问发现：v3.7 裁剪 commit 快照后 **P0 没有任何快照存储**
  （env commit 已裁、baseline = 镜像引用零存储、workdir 快照轴 P1），但工具清单
  仍在发放 snapshot_list/delete，§7.1 仍在描述 chunk hash 存储；full 快照在 env
  轴快照不存在后结构上不可能；policy/metrics/凭证工具的主体均不在 P0
- 备选方案:
  - 保持工具清单全集 + 脚注标注阶段: 拒绝 — 未实现工具混在清单里，Agent 客户端
    无法区分"能用"与"预告"，接口面撒谎
  - 工具清单按阶段分组: 采纳 — P0 组（10 个）明确可实现可测试；P1/P2 组为预告
  - full 快照保留字段: 拒绝 — 结构上不可能（env 快照不存在），删除逻辑而留字段
    是僵尸实体；P1 评估后按真实需求重启
- 决策: ① 工具清单按 P0/P1/P2 分组（P0 = resource_* ×3 + shell/file + 进程手×4 +
  revert + state_*×2 + status + env）；② §7.1 标 P1 留档，full/baseline/excluded
  三字段删除；③ §7.2 revert 分 P0 三步（镜像重建）/P1 五步（快照回滚）；④ §6 事件
  类型按阶段分组，file_op 并入 tool_call detail；⑤ §12 WebSocket 降评估项
- 影响: P0 工具面 = 10 个左右，每个有消费者；快照子系统从"P0 收窄"变为"P0 不存在"；
  P0-m1 已建的 pkg/snapshot 骨架保留（契约无害），实现随 P1
- 日期: 2026-11-24

### AD-016: 远程伴生服务——协议对话取代隧道透传，断线从试错变感知（v3.14）

- 状态: Accepted
- 背景: sandbox-mcp（Python 前身）远程模式是瘦客户端——SSH 隧道透传 PTY，Windows
  代码页/GBK 编码/PowerShell 管道无 Ctrl-C 等坑全在本地端逐平台适配，异构追不完；
  且 SSH 断线无连接状态概念，Agent 只能试错（不断开新 powershell），烧上下文。
  根因：健壮性逻辑放错了位置——远端异构性应由**远端同构程序**封装（vscode
  Remote-SSH 语义），而"远端部署任意程序"要求自包含静态二进制 ⇒ **Go 是架构级
  必要条件**，非偏好
- 备选方案:
  - 瘦模式继续逐平台适配: 拒绝 — 天花板已到（sandbox-mcp v0.3 实证），每平台
    一批坑，永不收敛
  - 伴生服务（协议对话）: 采纳 — 两端同构，编码/信号/PTY 语义协议层对齐
  - 全自动部署伴生: 部分采纳 — 体验好但"往服务器部署常驻 daemon"信任门槛高；
    默认半自动（scp + 打印启动命令 + 人工确认），全自动为配置开关
- 决策: ① ssh backend 主模式 = 伴生服务（sandboxd-lite Go 静态二进制），瘦模式降级
  bootstrap（送二进制/拉起/握手/退役）；② 会话与连接解耦（tmux 语义，断线不杀会话）；
  ③ 断线五层协议（心跳探测/connected 状态/立即失败反馈/会话解耦/自动重连自愈，
  §10.0）；④ 执行语义继承 sandbox-mcp 已验证设计（PTY/原子写/渐进发现/敏感路径
  警告），sandbox-mcp 远程模式冻结新特性（Python 版 = 单机身体）
- 影响: §1 分层补远端、§10.0 新增、Backend 契约加 health()；P1 路线图加伴生服务
  （第一个硬仗）；差异化定位：接入**用户已有机器**的远程身体（E2B/Daytona 的远程
  是平台托管沙箱，不接入既有主机——伴生架构是该赛道的独有路径）
- 日期: 2026-11-24

### AD-017: 身体事件流 = 拉模式订阅，不使用 MCP notification 推送（v3.15）

- 状态: Accepted
- 背景: 工具面原为纯拉，Agent 对进程死亡/env 崩溃/断线的感知延迟 = 轮询间隔。
  MCP 协议有 server → client notification 能力（resources/subscribe 等），但其
  客户端支持度参差（Claude Desktop 等处理不一，推了可能无人收）；且推送接收方
  是 Agent 的 context window——推送 = 平台往 Agent 脑内塞消息，打扰不可控
- 备选方案:
  - MCP notification 推送: 拒绝 — 客户端生态未成熟（可靠性风险）+ 打扰权错位
    （平台替 Agent 决定何时被打断，违反"心智归 Agent"）
  - 纯轮询（维持现状）: 拒绝 — 长任务感知延迟大，Agent 要自建轮询循环烧上下文
  - 拉模式订阅（events 游标 + 长操作阻塞等待）: 采纳 — 零协议依赖，两个机制都
    有已上线先例（audit_query 游标、shell_exec(wait=)）
- 决策: ① `events(after_seq)` 工具 = §6 审计流的窄化视图（身体相关事件），增量
  游标 = 订阅；② `process_logs(wait=true)` 阻塞等待语义（新输出/进程退出/超时）；
  ③ 不新增事件源（审计流复用）；MCP 原生 notification 与 WebSocket 同批留待
  客户端生态成熟后评估
- 影响: §4.4 新增、工具清单 +1（events）、process_logs 加 wait 参数（后随 AD-020/021 演进收缩）；感知权 =
  拉模式（Agent 决定何时看、一看即得），与 v3.14 断线五层的 L2 状态迁移闭环
  （events 可增量消费状态迁移）
- 日期: 2026-11-24

### AD-018: description 内嵌拉取指引——用 100% 可达面编码感知习惯（v3.16）

- 状态: Accepted
- 背景: 四家客户端源码实证（opencode / deepseek-harness / hermes / Claude Desktop 生态）
  确认：唯一被普遍消费的 server → client 通知是 `tools/list_changed`；`resources/updated`
  无处理器或 stub；`notifications/message` 静默丢弃（hermes 源码注释自证 SDK 默认行为
  = discard）。主动推送需要逐运行时适配（opencode 自建 events scheme / dsh+hermes
  只认工具面变化）——适配成本高且仅覆盖部分客户端。而工具 description 是 MCP 协议
  的 100% 可达面：必然注入模型上下文，全客户端一致
- 备选方案:
  - 逐运行时推送适配层（notify.Sink 接缝 + opencode-events/tools-list adapter）:
    暂缓 — 无真实消费者前不施工（AD-004）；触发条件 = P2 出现"长任务轮询烧上下文"
    的真实用户诉求
  - 全量工具 description 加拉取指引: 拒绝 — 11 个工具全加 = 上下文与行为双重浪费，
    非状态敏感操作（resource_create/snapshot 等）无此需求
  - 状态敏感工具 description 加一行指引: 采纳 — process_logs/process_signal（进程
    可能已死）、shell_*/file_*（machine 可能断线）、state_get（唤醒首调配对 events）
- 决策: 4 类状态敏感工具的 description 内嵌一行拉取指引（约 +40 tokens/工具面）；
  与错误内嵌指引互补（description = 事前习惯，L3 立即失败错误 = 事中发现）；
  诚实条款：指引提高拉取概率但不担保依从性，可靠性兜底仍是 L3 + wait 阻塞语义
- 影响: §4.4 新增小节、P0-m4 MCP server 实现时 description 模板随工具面落地；
  推送适配层（notify.Sink）留待 P2，与 §14.1 计量接缝同构（核心发流，下游适配）
- 日期: 2026-11-24

### AD-019: 响应捎带通知——感知终态，拉取动作吸收进既有调用（v3.17）

- 状态: Accepted
- 背景: v3.15 主动拉（烧显式往返）→ v3.16 description 指引（依从性不保证）——
  两条路径都让 Agent 付出感知成本。第三条路：工具调用本来就有往返间隙，服务端在
  **响应里附带滞留事件** = 拉取动作被既有调用吸收，Agent 零新增成本；工具响应是
  全客户端 100% 消费的第二个协议保证面（第一个是 description）
- 备选方案:
  - 维持纯拉（v3.15）: 降级 — 感知依赖 Agent 主动性，长任务延迟大
  - 仅 description 指引（v3.16）: 降级为辅助 — 依从性中等，不担保
  - 响应捎带: 采纳 — 平台侧保证每次调用必然携带，可靠性不依赖任何单一依从性
- 决策: ① 每次工具响应附带 pending body-events（无则省略 = 零开销）；② 投递水位
  （delivery watermark）per-connection 由平台维护，Agent 无需传参——连接是传输
  事实非心智概念，不违反 v3.9 边界；③ payload 防淹没（积压 > N 条压缩为摘要 +
  events 提示）；④ description 指引降级为"主动回看历史"的辅助入口
- 影响: §4.4 捎带小节；P0-m4 MCP server 实现时在工具响应组装层统一注入（单点
  实现，非逐工具）；感知栈四通道合成：捎带（必然即时）→ wait 阻塞 → events 回看
  → description 习惯——感知可靠性不再依赖任何单一通道
- 日期: 2026-11-24

### AD-020: 进程手收缩为 exec_async 单工具——观察干预归 shell（v3.18）

- 状态: Accepted
- 背景: 连环质疑拆解 process_*：① `ps`/`tail`/`kill` 是 shell 现成命令，包装它们
  = 冗余工具烧 schema；② `process_logs(wait=true)` 的"等待期间保持可操控"是
  错觉——MCP 工具调用同步，wait 挂起期间 Agent 同样冻结，与阻塞执行无异（唯一
  剩余差异"结构化结局"价值不足以撑独立语义）；③ process_list 的账本视图价值
  （"我起了什么、活着吗"）可由 exec_async 返回值 + 捎带死亡事件 + status 进程
  存活字段覆盖；"我起了什么"本质是记忆问题，归 state doc（AD-008 唤醒闭环）
- 备选方案:
  - 保留三工具 + 砍 wait 参数: 部分 — list/logs 仍与 shell 冗余
  - 收缩为 exec_async + process_list 两工具: 不彻底 — list 的读账场景已被
    捎带+status 覆盖
  - exec_async 单工具，账本退化为一等数据: 采纳 — 平台记账（事实），读账走
    status/捎带，无专门读账工具
- 决策: process_list/process_logs/process_signal 全砍；exec_async 保留（核心价值
  = 非阻塞地起，不占 shell 会话）；句柄持久化 SQLite 保留；死亡事实走捎带通知；
  观察干预归 shell（tail/ps/kill——userland 命令是 Agent 与生俱来的手）；跨机
  聚合真需求出现时 status 扩字段而非恢复工具
- 影响: §4.2 收缩、工具清单 P0 面 11→8（resource×3 + shell/file + exec_async +
  revert + state×2 + status + events + env）；§4.4 感知栈 wait 通道移除（三通道：
  捎带→events→description）；AD-017/018/019 的 process_logs 表述作废；
  判决标准沉淀：**平台只造 shell 做不到的原语，shell 能做的一律不造**
- 日期: 2026-11-24

### AD-021: revert 出局——动词还原为名词的属性（v3.19）

- 状态: Accepted（Supersedes §7.2 五步流程与"保留 revert"论证）
- 背景: 三轮质疑链：① container_build 与 revert/create 重叠（都是"从镜像 run 容器"）；
  ② revert 对 docker 是借来的快照术语，摩擦大；③ **致命一击——workdir 不变量
  （workdir = f(work_home, env_name)）成立后，"Agent 自己编排 create+挂卷+改绑定"
  的保留论证失效**：create 的 schema 里本来就没有挂卷参数，因为根本不需要——
  不变量替它做了。同名 create 即完整自愈
- 备选方案:
  - 保留 revert 工具: 拒绝 — 剩余差异仅为审计事件类型，不值一个工具 + 五步流程
  - revert 并入 create 参数: 拒绝 — 同样冗余且名字撒谎
  - 自愈 = remove + 同名 create + 三个既有机制: 采纳 — workdir 不变量（名字即地址）+
    注册表 name 键控（身份复活）+ 审计 resource_recreated 事件（自愈语义标记）
- 决策: revert 工具出局；§7.2 重写为"自愈 = 同名重建"；NFR-1/2 改挂自愈重建路径；
  事件类型 resource_recreated 进入 P0 组
- 影响: 工具面 -1；§3.1 并发仲裁措辞、§7.0/7.1/7.3 同步；给 Agent 的一句话：
  "拆了重搭，东西还在原地；换镜像 = 拆重搭时给新镜像"
- 日期: 2026-11-24

### AD-022: backend 双轴矩阵 + 工具命名去泛型（v3.19）

- 状态: Accepted
- 背景: ① P0 范围修正为 docker×linux + ssh×linux + ssh×windows（瘦模式过渡），
  P1 增 companion×android（安卓是伴生架构的决定性用例：无原生 Docker、shell 非标配，
  瘦客户端不可用）；② "resource_create 承担太多"担忧验算后发现 P0 参数面仅
  (name, image?)——最薄工具；真膨胀点在 P1 多类型，解法是按类型分裂兄弟工具
- 备选方案:
  - 泛型 resource_create(type=...) 服务多资源: 拒绝 — 参数并集爆炸（AWS 泛型
    Create 之误）；"一切皆资源"应统一在数据层（注册表/ID/审计），非动词层
  - P0 resource_create、P1 分裂: 不一致 — P0 就一个类型，泛型前缀名不副实
  - **env_create** 直呼其名，P1 加 policy_set 等兄弟: 采纳 — 名字诚实且分裂路径顺滑
    （**后随 AD-027 进化为 container_create——族重排后被操作物命名更诚实**）
- 决策: ① §10 重构为 backend（docker/ssh/companion）× OS（linux/windows/android）
  双轴矩阵，OS 差异 = 伴生内部实现分支，协议层 kill(handle)/wait OS 无关；
  ② 工具名 resource_create → **env_create**（P0 唯一类型）；③ P0 ssh 瘦模式
  最小过渡分级（connect/阻塞 exec/file(SFTP)/编码层抄 ✅；async/账本 ❌ 等伴生）；
  ④ 搬运原则：代码债便宜就还，架构债必须等终态
- 影响: §10/§10.0 重构、P0-m3 拆 m3a/m3b、P1 路线图（伴生提前为第一个硬仗）；
  resource_ 前缀仅存 inspect/remove（P1 类型分化时再定去留）
- 日期: 2026-11-24

### AD-023: 否决批——share 共享目录、admin god-mode、env 元工具（v3.19）

- 状态: Accepted（env 元工具部分被 AD-024 部分取代：help/渐进发现否决维持，
  lifecycle 动词以专用工具回归）
- 背景: Python 对照轮收获的三个候选机制逐一拷问：① 共享目录（ro 全棵 + rw overlay）
  精巧但 backend 专属（bind-mount 到 ssh/vm 不存在）+ DNS 网络下 shell 可代
  （http.server + curl）+ 容器化部署再收一份双路径税；② admin /host 全局视图
  容器服务于"跨机清理"，与 revert/自愈语义重复且是安全负债；③ env(action=help)
  渐进发现在 7 工具薄面下节省 < 元工具自身成本（description 100% 注入优于按需 help）
- 备选方案:
  - share 留 P1 评估: 拒绝 — 否决理由充分（非"暂无场景"而是"有更好的替代"），
    留评估位 = 给精巧但错误的方案留门
  - admin 容器: 拒绝 — 跨机操作 P1 走伴生正门
  - env 渐进发现: 拒绝 — 它的正确继承物是"tools/list 保持小"，靠工具面本身小达成
- 决策: 三者否决，否决理由存档（防止未来看着 Python 版"设计挺精巧"又搬回来）；
  env 互联 = DNS 网络（§13.2，容器名即主机名，不开宿主端口——继承 Python 版立场）；
  工具面 7 个终态
- 影响: §13.2 新增、工具清单 -1；Python 对照轮的最终资产清单 = PTY 会话语义 +
  原子写 + SFTP + 编码层 + DNS 网络 + 显式 tag 防碰撞（commit P1 回归时）——
  其余（share/admin/渐进发现/瘦客户端终态）全部否决且有档
- 日期: 2026-11-24

### AD-024: 生命周期动词回归 + machine 参数退场（v3.20）

- 状态: Accepted（Partially supersedes AD-023）
- 背景: scheme 复审发现两处结构缺陷。① AD-008 承诺"睡眠/唤醒由 Agent 自主决定"，
  但工具面只有 create/remove——Agent 无任何工具触达 stopped↔running 迁移（NFR-8
  "睡眠不烧执行时长"是空头支票）；AD-023 埋 env 元工具时把 action 调度和 help 用途
  一刀切陪葬了。② shell_exec/image_build 带 machine? 参数，但 P0 单机形态下所有
  env 都在本机 daemon——参数无消费者（AD-004 违规），且 machine 概念全文 11 处
  出现却从无定义，connect 工具也不存在
- 备选方案:
  - env(action=sleep|wake) 元工具回归: 拒绝 — AD-023 否决的是元工具形态，不是动词；
    sleep/wake 直呼其名与身体隐喻一致（create/sleep/wake/remove 四动词完整一生）
  - 只加 env_sleep/wake 其一: 拒绝 — 睡眠无唤醒 = 单程电梯，AD-008 双向自主缺一半
  - machine? 保留占位: 拒绝 — 无消费者的参数是 schema 噪音 + 幽灵语义；
    P1 ssh 落地时随 connect 族一起设计，届时参数有真实语义（machine = 连接目标）
  - write_stdin 三件套复活（sudo 场景触发）: 拒绝 — sudo 是已知时点 stdin，
    管道（echo PW | sudo -S）精确解决；三件套的独特价值是未知时点中途交互，
    那类程序对 Agent 是反模式且 Windows UAC 连 PTY 都驱动不了（Python 版从未解决）
- 决策: ① +env_sleep/env_wake（lifecycle 族 5 工具，**AD-026 撤销**）；② 全部签名删
  machine?（**AD-026 部分推翻——P0 含 ssh，目标选择型签名回归 machine?**）
  （P1 随 connect 族回归）；③ §4.0 跟随式默认两维拆分——P0 默认维度只有 env
  （workdir 可多开，§4.1），machine 维度 P1；④ §4.0.1 sudo 管道注记进
  shell_exec description
- 影响: 工具面 16→18（7 族不变）；跟随式默认的"驻留型"增补 env_wake，
  env_sleep 是探取型（睡觉不换台）——**随后被 AD-026 撤销**（见下）
- 日期: 2026-11-25

### AD-026: scheme 再审——sleep/wake 移除、image_list 补缺、machine 参数回归（v3.21）

- 状态: Accepted（Supersedes AD-024 之 ①③；AD-024 的 sudo 注记与 write_stdin
  判决维持）
- 背景: scheme 复审第二轮，三个新事实：① 用户裁定 sleep/wake 非关键功能——
  两个工具 = 两份 schema token 上下文税，且 P0 无配额执行器（睡眠"省执行时长"
  是空头收益），ssh 物理机更无"睡机器"语义（docker backend 专属），AD-004
  检验不通过；② env_create(image?) 缺选型依据（工具后更名 container_create，AD-027）——Agent 不知道有哪些镜像可用
  （Python 版 list_images 有消费者）；③ **P0 不是单机形态**——双轴矩阵早有
  ssh×linux/windows（AD-022），AD-024"单机无消费者"判决事实错误
- 备选方案:
  - sleep/wake 保留 + ssh 报 unsupported: 拒绝 — capability 声明可以救语义，但救不了
    上下文税；无消费者的动词不进面（奥卡姆）
  - sleep/wake 实现为平台内部路径（自愈重建的 stop 步骤）: 采纳 — 状态机 stopped 态
    保留，迁移由内部驱动，Agent 视角只有 create/inspect/remove
  - image_create 缺省值绑定默认镜像: 采纳搭配 — image 缺省 = 该 env 当前镜像
    （同名自愈不换身体），换身体 = 显式给 image 参数
  - machine_connect 工具进 P0: 拒绝 — 远程注册走 sandbox.yaml 配置声明（Python 版
    同构：SSHTarget 静态配置），动态注册的消费者是伴生架构（P1）；P0 工具面
    machine 参数只做**目标选择**（选择已声明的 machine），注册是配置问题
  - set_default 保留: 拒绝 — 跟随式默认已覆盖（§4.0），"不够用时再评估"的尾巴
    也剪掉；真不够用是 P1 多形态验证后的事
  - policy_*/snapshot_* 留 P1: 拒绝 — 网络策略的消费者是多租户治理（P2），
    快照轴消费者是多 backend 场景（P2）；单机单用户 P0/P1 用不上，降 P2
- 决策: ① env_sleep/env_wake 移除（P1 随配额执行器回归，记录在 P1 路线图）；
  ② +image_list()（image 族 2 工具，**后随 AD-027 更名 container_images 并入 container 族**）；③ machine? 参数回归全部目标选择型签名
  （shell_exec/shell_new/file_*——P0 = 已声明 machine 的选择器）；④ policy_*/
  snapshot_*/set_default 全部挪 P2；⑤ machine_list 不设——status 全景返回清单
  增补 machines 维度（含连接状态），专门枚举工具与全景快照重合，奥卡姆砍；
  machine_connect/disconnect 降为**条件触发**（"任务中途接新机"场景出现才建，
  默认答案 = 配置声明 + 热加载——Python 版至今无连接工具活得很好）
- 影响: 工具面 18→17（6 族）；NFR-8 验证阶段改 P2（配额执行器随治理身份）；
  P0-m3b 的 machine 注册 = sandbox.yaml SSHTarget 声明式配置（抄 Python 版
  config.py 结构）；AD-008 的"睡眠省时长"承诺顺延至 P1 配额执行器；
  §4.3 status 返回清单增补 machines 维度；P1 路线图 machine_list 划除
- 日期: 2026-11-25 Python 对照终审——特性映射表与豁免条款（v3.20）

- 状态: Accepted
- 背景: v3.19 只立了搬运原则（代码债便宜就还/架构债等终态），未做全工具面系统
  盘点。本轮逐项对照 sandbox-mcp 13 工具 + 14 底层特性，补齐判定存档
- 判定（抄）:
  - **控制面重启对账**（_reattach_existing + list_managed_containers 语义）:
    sandboxd 重启 ≠ 身体丢失——容器在跑、注册表有记录、缺的是桥接。启动时
    注册表 × daemon 实际容器比对（收编/标 error/孤儿列出不静默删）——§3.1 新条款
  - **原子写 + 编码归一化**（temp+mv、UTF-8 LF no BOM、BOM/GBK 四级检测、
    写后语法检查）: file_* 族底层，docker/ssh 两 backend 通用——m3a 即抄
  - **敏感路径警告**（safety.py denylist，advisory 不阻断）: file_write 返回带
    warning 字段，纯函数孤立易移植——m3a 即抄
  - **sudo 管道注记**（§4.0.1）: 零代码，description 携带
- 判定（不抄，有档）:
  - shell_read/wait=false 三件套: v3.18 死刑维持；**豁免条款**——若 P1 伴生工作
    中出现真实的未知时点交互消费者（非 sudo/非 REPL），三件套作为整体回归评估，
    以 Python 版为参考实现。防止将来重新考古
  - audit_query: Agent 消费面已有 events(after_seq)；人直接 sqlite3 读库。
    P1 复盘工具化时再评估（AD-004）
  - commit/logs/diff/history/stats: P1 评估/P2 metrics（观察干预归 shell）
  - write_stdin 单独搬运: 三件套的原子部分，见 AD-024
- 决策: 判定表存档于本 AD；m3a 实现清单增补（原子写/归一化/敏感路径）；
  m3b 实现清单增补（编码层照旧）
- 影响: §3.1（对账条款）、§4.0.1（sudo 注记）、§10.0（分级表补 3 行）；
  Python 前身的设计遗产至此全部清算完毕——该抄的有名有份，不抄的有档有据
- 日期: 2026-11-25