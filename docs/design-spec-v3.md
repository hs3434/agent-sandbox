# Design Spec v3 — Agent 操作现实的支点（原"Sandbox 平台"）

> 状态：v3.13 — 2026-11-24，**P0 施工基线**
> 修订：v3.1 恢复边界 = 本地态；v3.2 capability = 平台原语、用例中立；v3.3 spec 即预留；v3.4 第一性重定位（赋能操作，身体/手/支点）+ 快照策略（标准镜像优先）；v3.5 身体自治（进程手/睡眠唤醒/自状态文档，心智归 Agent）；v3.6 状态文档归本体（MCP 直连，记手的状态）；v3.7 commit 快照暂缓 + baseline=镜像引用 + 句柄持久化 + 凭证同钟；v3.8 status 工具（本体感觉与记忆分离）；v3.9 记忆键去 session 化；v3.10 记忆 = 单文档无键（锚点零信息量）；v3.11 Agent 自建凭证归属澄清；v3.12 凭证子系统降级 P2 预留；v3.13 奥卡姆清扫（工具面按阶段分组、full 快照降级、P0 零快照存储、事件类型分组、WebSocket 降评估）
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
控制面（MCP API + 资源模型 + 治理）→ 编排层（生命周期/恢复/归属）→ 数据面（Backend 执行器）
```

- 控制面/数据面分离：Agent 只见资源模型，不见底层实现
- 编排层承载跨资源治理：恢复调度、配额、任务归属
- 数据面只做执行 + 上报（结果、指标、状态事件）

## 2. 资源模型

### 2.1 资源标识

```
resource_id = "{tenant}:{type}:{name}"
例: hu:exec_env:rnaseq-worker-01 / hu:policy:net-default / hu:credential:db-password / hu:snapshot:before-deg
```

### 2.2 资源类型注册表

| type | capabilities | 生命周期 | CRUD 对称性 | 恢复机制 |
|---|---|---|---|---|
| exec_env | shell/files/snapshot（capability = 平台原语，非软件清单，见 §10） | 运行面状态机 | C/R/D + 重建式 U | 快照/回滚（重建成本高）；workdir 独立恢复轴（见 §7） |
| policy | rule 读改写 | 配置版本化 | 完整 CRUD | 版本化回滚（成本低） |
| credential | inject/recycle | 注入↔回收 | C/D、无 R、重建式 U | 重新注入（秘密不可快照） |
| snapshot | 只读元数据 | 创建→保留→轮转 | C/R/D | —（本身即恢复手段） |

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
- **睡眠/唤醒由 Agent 自主决定（AD-008）**：sleep = graceful stop（stopped，容器可写层
  保留，文件系统原样）；wake = start **续用**（非重建，进程不复活）——比 env revert
  （重建）便宜的日常续用通道。恢复阶梯：wake（续用）< 标准镜像重建（commit 快照 P1 评估）
- **唤醒失败降级链**：容器丢失（宿主重启/docker prune）→ wake 报错 → 自动走重建链
  （标准镜像/baseline + workdir 重挂 + 自状态文档仍在（MCP 直连，见 §4.1）——降级不失忆）- 策略时限默认仅计 running（睡眠不烧执行时长，占磁盘；磁盘配额 P1）
- 无 update：改镜像/限额 = 重建（不可变基础设施）
- **并发仲裁（P0 必需）**：每个 exec_env 持有编排层互斥锁——同一时刻只允许一个生命周期
  迁移或 revert 在途；第二个操作立即拒绝（409 conflict，非排队）。revert 内部五步视为
  一个临界区（锁覆盖 ①→⑤）。锁随 removed 释放；error 状态持有锁直到显式 reset/重建

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

resource_create / resource_inspect / resource_remove
shell_* / file_*（capability 校验后路由到数据面）
exec_async / process_list / process_logs / process_signal（进程手，P0-m3，见 §4.2）
revert（P0 = 标准镜像重建，见 §7.0；时点回滚随快照轴 P1）
state_get() / state_put(doc)（记忆文档：单文档无键，MCP 直连，见 §4.1）
status（本体感觉：聚合只读的身体事实，见 §4.3）
env(capabilities)
—— P1：policy_get / policy_set / policy_versions / policy_revert（网络策略落地随开）；
snapshot / snapshot_list / snapshot_delete（workdir 快照轴，§7.1）
—— P2：credential_inject / credential_recycle（无 read，见 §9）；metrics

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
网络位置（内网|外网）、凭证在场元数据（非值）。

- 与 §4.1 分工：**status = 本体感觉（永远真，平台算）**，**state doc = 记忆（可过时，
  Agent 写）**——status 是 state doc 的活体校准源
- 与 `resource_inspect` 关系：inspect = 单资源深查；status = 实例全景浅查。
  Agent 日常先 status 后按需 inspect，一次调用免多次往返
- 作用域：P0 单用户单实例 = 全实例即全景；P2 治理身份（§11 三层 Subject）落地时
  按 Subject 过滤——治理元数据不等于会话管理，届时也不引入 session 概念
- **唤醒降级链不失忆**：容器丢失走重建时，状态文档在平台侧 SQLite 键值存续——
  身体可以换，记忆不丢（比 v3.5 的 workdir 方案更早可用：先有感觉，再有身体）

### 4.2 进程手（per-env 句柄，非全局资源类型）

- `exec_async`：起后台进程，返回 `{pid, handle_id}`；进程注册在 per-env 句柄表，
  **句柄持久化 SQLite（P0-m2）**——控制面重启句柄不丢，`process_logs` 游标可续
- **容器停止 → 异步进程全灭**（stop/snapshot 链路同理）：句柄显式标 dead，不静默清理
  （Agent 可见死亡事实，对齐自状态文档记忆）
- `process_list / process_logs(尾部+游标) / process_signal`：观察与干预
- **平台不复活进程**（诚实条款）：容器停止 = 进程消亡；进程复活 = 重建应用状态，
  复杂且易腐——"该重启哪些进程"由 Agent 依自状态文档决定，平台只保证身体持久性
- 不做全局进程资源类型（AD-004 纪律）：句柄表 per-env 即可，出现跨 env 进程治理
  需求时再资源化
- 端口暴露/预览（port_expose）：P1 记接缝，P0 不实现（单机形态暂无消费场景）

## 5. 权限与策略模型

```
策略 = 主体 × 资源 × 动作 × 条件
主体: tenant → task → agent session（三层）
默认放行 + 显式收紧（recovery-first 兼容）；default deny 仅用于 credential_read 结构性禁用
```

## 6. 审计子系统

事件模型：{id, ts, tenant, subject, resource_id, action, outcome, duration_ms, detail(脱敏)}
类型（按引入阶段分组，勿预建无消费者的事件类型）：
- P0：tool_call / resource_created / resource_removed / error
- P1：snapshot_created / snapshot_reverted / policy_change / policy_blocked
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
- **时间宽容**：恢复的正确性优先于速度，NFR-1 的 revert 延迟只约束"标准镜像重建"
  路径（见 §15）

- **环境是牛，工作目录是资产**：env revert 丢弃运行时，workdir 原样保留并重挂到新实例——
  环境搞坏 ≠ 丢工作成果（对齐 Docker commit 排除 volume / K8s 重建保留 PVC 的行业语义）
- workdir 挂载路径属于 ExecEnvSpec（不可变，create 时定）；快照只记 `excluded: [workdir]` 元数据
- **workdir 永不进 env 快照**（env 快照把新工作冻结进旧时点 = revert 即丢工作）；
  与凭证不进快照同构但理由不同：凭证为安全，workdir 为数据完整
- 跨节点约束（P2）：docker volume 本机绑定；多节点时 workdir 后端换网络卷，P0 单机无此问题

两轴恢复矩阵：

| 故障模式 | 恢复动作 | 机制 |
|---|---|---|
| 环境坏、数据好（最常见） | env revert，workdir 原样重挂 | env 快照 |
| 数据坏、环境好 | workdir restore（恢复到数据快照点/git 点） | workdir 快照 |
| 都坏 | 全量恢复：env revert + workdir restore | 全量快照对（同一时刻成对打） |

**恢复边界 = 本地态**（哲学条款）：两轴快照只恢复本地状态（容器层/文件系统/工作目录）。
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

### 7.2 revert 流程（P0 = 镜像重建三步；快照点回滚随快照轴 P1 扩为五步）

P0（env 轴，无快照存储）：
1. 校验（存在/归属/权限）→ 2. 停止实例 + 丢弃容器 → 3. 从标准镜像重建 + **重挂原 workdir 卷**

P1（有快照后扩为五步）：
1. 校验（存在/归属/权限/scope 匹配）
2. 停止实例（graceful → 超时强杀）+ **quiesce workdir**：fsync 挂载卷（crash-consistent 即可，日志文件系统可恢复；应用级 flush hook 可选）
3. 从快照载体重建 env，**重挂原 workdir 卷**（非快照内）
4. 恢复非状态资源：策略从 policy 当前版本重新绑定；凭证重新 inject（P2）；workdir 所有权修正（uid 映射）；标签沿用
5. 校验（healthcheck）+ 审计

失败补偿链（②③ 失败窗口，两版通用）：
- ② 失败：实例已在停路径，重试强杀；workdir 卷不受影响
- ③ 重建失败：重试一次 → 仍失败则从 **标准镜像（baseline 引用）**重建 → 镜像也丢 → 环境标 error，
  保留旧实例 backend 残留与 workdir 卷供人工排查（workdir 永不在补偿链中被销毁）

要点：revert 恢复"环境状态"，配置/凭证/workdir 永远走各自活通道——快照永不携带策略、秘密和新工作。

### 7.3 策略（按可重建成本分层保留）

| scope | 存储 | 保留 | 触发 |
|---|---|---|---|
| env | 标准镜像（Dockerfile） | 标准镜像由 Dockerfile 版本管 | 手动 snapshot 调用（env 轴快照暂缓，见下） |
| workdir | 文件级去重（chunk hash） | N=20 轮转（便宜可高频） | 手动 + git 仓库可选 auto-stash（P1 优化） |
| full | env+workdir 成对 | N=2 | 显式调用（重型检查点） |

**baseline := 标准镜像引用（v3.7 简化）**：标准镜像优先后，创建时 env == 标准镜像，
自动 baseline 的 commit 快照是零信息量复制——baseline 直接定义为标准镜像引用
（零拷贝零成本），补偿链兜底 = 从标准镜像重建。
**commit 快照暂缓（v3.7 裁剪）**：引入独立快照流（stop/commit/tag/轮转）复杂度不匹配
当下收益——P0 的 env 轴恢复 = wake 续用 + 标准镜像重建两档；Agent 在 env 内装了
镜像之外的增量依赖且需要保留时，正向工作流是**写进 Dockerfile**（声明式真相），
回滚重建自然携带。commit 快照留作 P1 评估项（出现"增量状态频繁且不值得 Docker 化"
的真实场景再引入）。

引用计数：被 revert 链引用的快照不轮转；跨轴组合恢复时 full_pair 双方一起计数。

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
| 跨 env 重建存活 | workdir（资产轴） | env revert 不丢，与代码同为资产 |
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

```python
class Backend(ABC):
    capabilities: set[str]
    create/start/stop/remove
    exec / read_file / write_file
    snapshot(exclude_secrets=True) / restore(ref)
    metrics()
```

| Backend | capabilities | snapshot 实现 | 隔离级别 | 适用 |
|---|---|---|---|---|
| docker | shell/files/snapshot | wake 续用 + 标准镜像重建（commit 快照 P1 评估） | 容器（共享内核） | 默认：开发/执行，秒级启停 |
| ssh | shell/files | 无（诚实声明） | 无（复用既有主机） | 接入既有物理机/远程开发机 |
| vm (future) | shell/files/snapshot | libvirt/qemu 外部快照（VM 级 CoW）或存储层快照（qcow2 内部快照/切片） | 硬件级虚拟化（独立内核） | 需要内核隔离/不同 OS/系统级实验（内核模块、网络栈、多发行版矩阵） |
| k8s (future) | shell/files/snapshot | Pod 重建 + PVC 快照 | 容器编排（多节点） | 平台托管形态：多节点调度、PVC 原生两轴快照 |

**vm 与 docker 的选型语义**：默认 docker（快、轻、秒级 revert）；当任务需要**独立内核**
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
| P0-m2 | SQLite 审计 sink + 内存注册表 + 状态机（含并发仲裁锁） | §3 §6 §13 |
| P0-m3 | docker backend：create/exec/snapshot（标准镜像重建，commit P1 评估）/restore + 两轴挂载 | §7 §10 |
| P0-m4 | sandbox.yaml 配置装配 + MCP server（工具面按 P0 子集暴露） | §4 §13 |

P1：策略资源化（网络边界）+ 审计脱敏 + workdir 快照轴 + commit 快照评估
P2：三层身份 + metrics + vm/k8s backend 评估

验证矩阵（`docs/verification/feature-test-matrix.md`）在 P0-m3 首个可运行 backend
落地后建立——0 代码阶段无矩阵可写，预造空转。测试层级约定沿用 U/UI/P/D/M
（P0 无 UI/设备，主要 U 契约测试 + P 管道级集成）。

---

## 15. 可测量 NFR（哲学 → metric 对照）

定性哲学翻译为可测量 NFR（没有 metric 的原则是口号，不是需求）：

| ID | 哲学（定性） | 可测量 NFR | 目标值 | 测量方式 |
|---|---|---|---|---|
| NFR-1 | 恢复优先 | revert 延迟（标准镜像重建路径） | p95 < 5s（commit 路径宽容，见 §7.0） | revert 流程计时 |
| NFR-2 | 恢复优先 | revert 成功率 | > 99%（baseline 兜底后） | 补偿链各分支计数 |
| NFR-3 | 能力释放 | 命令默认放行率 | = 100%（命令策略默认空） | policy.Decision 采样 |
| NFR-4 | 执行环境不可变 | update 接口数 | = 0（改配置 = 重建） | API 面审计 |
| NFR-5 | 凭证最小暴露 | 平台通道快照中明文密钥数 | = 0（范围 = 平台注入物；Agent 自建 userland 凭证不在平台承诺内，见 §9） | 快照载荷脱敏校验 |
| NFR-6 | 凭证最小暴露 | credential_read 工具数 | = 0（结构性禁用） | 工具面审计 |
| NFR-7 | 审计 = 复盘 | 事件可回溯率 | = 100%（脱敏后） | audit Query 覆盖 |
| NFR-8 | 配额 = 保活 | 单任务超限不影响其他任务 | 无饥饿（P1 验证） | 配额测试 |
| NFR-9 | 赋能操作 | env 冷启动延迟（create→running） | p95 < 10s（标准镜像有缓存） | create 流程计时 |
| NFR-10 | 赋能操作 | exec 往返延迟（同步 shell） | p95 < 500ms | exec 计时 |

P0 阶段只强制前 6 条中可测的（revert/放行/不可变/冷启动/exec——凭证条随子系统
P2 落地）；其余随 P1/P2 落地。

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