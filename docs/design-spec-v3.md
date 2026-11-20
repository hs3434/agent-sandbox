# Design Spec v3 — Sandbox 平台参考架构

> 状态：draft — 2026-08-14
> 依据：云厂商资源管理哲学（AWS/Azure/GCP/阿里云/腾讯云收敛）+ Agent 执行场景分析
> 核心哲学：
> 1. 一切皆资源，治理统一，操作分化（capability 声明）
> 2. 控制面/数据面分离
> 3. 执行环境不可变（update = 重建，与回滚天然一致）
> 4. 恢复机制按"状态可重建成本"分层：快照（高成本）/ 版本化（低成本）/ 注入（秘密）
> 5. 凭证最小暴露：永不落快照、结构上不存在 read
> 6. 审计 = 复盘工具（Agent 自省），不仅是合规记录
> 7. 配额 = 保活机制（防单任务饿死系统），非能力限制

## 0. 沙箱语义重定义

传统 sandbox = 拦截、限制、default deny（笼子）。本项目重定义 sandbox 为：
**不拦任何命令的执行舞台** —— 能力最大化，安全由"环境基线 + 随时回滚"兜底。

- 对外叙事统一用此定义（README tagline 已对齐："可释放能力、可回滚状态、可审计行为"）
- 仓库名 `agent-sandbox` 保留（生态语境准确：E2B/Daytona 同义）；产品品牌名与仓库名解耦，独立命名（商业策略见内部文档）

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
| exec_env | shell/files/exec（capability = 平台原语，非软件清单，见 §10） | 运行面状态机 | C/R/D + 重建式 U | 快照/回滚（重建成本高）；workdir 独立恢复轴（见 §7） |
| policy | rule 读改写 | 配置版本化 | 完整 CRUD | 版本化回滚（成本低） |
| credential | inject/recycle | 注入↔回收 | C/D、无 R、重建式 U | 重新注入（秘密不可快照） |
| snapshot | 只读元数据 | 创建→保留→轮转 | C/R/D | —（本身即恢复手段） |

### 2.3 元数据（共享）

```yaml
resource_id, type, capabilities, backend, status,
tags: {tenant, project, purpose}, limits: {cpu, memory, pids},
created_at, owner(session/任务)
```

## 3. 生命周期状态机

### 3.1 执行环境（运行面）

```
created → starting → running → stopping → stopped → removing → removed
                     └→ error（超时/失败，可重试或重建）
```
- 迁移幂等（重复调用同状态）
- 超时驱动：starting 30s / stopping 60s
- 无 update：改镜像/限额 = 重建（不可变基础设施）

### 3.2 配置（版本化）
```
created(v1) → modified(v2) → ... → revert(v1) 生成新版本
```
### 3.3 凭证
```
injected → active → recycled（任务结束）/ expired（超时兜底）
```

## 4. 控制面工具清单（MCP）

resource_create / resource_inspect / resource_remove
policy_get / policy_set / policy_versions / policy_revert
credential_inject / credential_recycle（无 read）
snapshot / revert / snapshot_list / snapshot_delete（均带 scope: env|workdir|full 参数，默认 env）
audit_query / metrics / env(capabilities)
shell_* / file_*（capability 校验后路由到数据面）

## 5. 权限与策略模型

```
策略 = 主体 × 资源 × 动作 × 条件
主体: tenant → task → agent session（三层）
默认放行 + 显式收紧（recovery-first 兼容）；default deny 仅用于 credential_read 结构性禁用
```

## 6. 审计子系统

事件模型：{id, ts, tenant, subject, resource_id, action, outcome, duration_ms, detail(脱敏)}
类型：tool_call/file_op/network_decision/policy_change/snapshot_created/snapshot_reverted/credential_injected/credential_recycled/resource_created/resource_removed/policy_blocked/error
脱敏：秘密值存 sha256 指纹，明文永不落库
用途：Agent 复盘（自省决策链）、任务失败定位、安全追溯

## 7. 快照子系统

### 7.0 两轴恢复模型（env / workdir 正交）

exec_env 的可恢复状态拆为两个**正交轴**，各有独立快照流与保留策略：

```
exec_env
├── env 态（镜像层 + 容器层/VM 磁盘） → env 快照（CoW：docker commit / VM 磁盘外部快照）
└── workdir（/workspace 挂载卷）      → workdir 快照（文件级，去重存储）
```

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
docker 沙箱同理（外部交互占比小而已），浏览器尤甚（主要交互即远端状态，见 §10）。

### 7.1 模型
{id, resource_id, name, created_at, type: baseline|manual|auto, scope: env|workdir|full, size_bytes, backend_specific, status, excluded: [credential_ids, workdir]}

- scope=env：仅环境态（默认）；scope=workdir：仅工作目录；scope=full：同一时刻成对打（全量检查点）
- full 快照在注册表内是两条关联记录（env + workdir），`full_pair_id` 关联，恢复时可组合

### 7.2 revert 流程（五步，env 轴）

1. 校验（存在/归属/权限/scope 匹配）
2. 停止实例（graceful → 超时强杀）+ **quiesce workdir**：fsync 挂载卷（crash-consistent 即可，日志文件系统可恢复；应用级 flush hook 可选）
3. 从快照载体重建 env，**重挂原 workdir 卷**（非快照内）
4. 恢复非状态资源：策略从 policy 当前版本重新绑定；凭证重新 inject；workdir 所有权修正（uid 映射）；标签沿用
5. 校验（healthcheck）+ 审计

失败补偿链（②③ 失败窗口）：
- ② 失败：实例已在停路径，重试强杀；workdir 卷不受影响
- ③ 重建失败：重试一次 → 仍失败则从 **baseline 快照**重建 → baseline 也坏 → 环境标 error，
  保留旧实例 backend 残留与 workdir 卷供人工排查（workdir 永不在补偿链中被销毁）

要点：revert 恢复"环境状态"，配置/凭证/workdir 永远走各自活通道——快照永不携带策略、秘密和新工作。

### 7.3 策略（按可重建成本分层保留）

| scope | 存储 | 保留 | 触发 |
|---|---|---|---|
| env | CoW（docker commit tag） | N=5 轮转 + baseline 引用计数 | 自动 baseline（创建时）+ 手动打点 |
| workdir | 文件级去重（chunk hash） | N=20 轮转（便宜可高频） | 手动 + git 仓库可选 auto-stash（P1 优化） |
| full | env+workdir 成对 | N=2 | 显式调用（重型检查点） |

引用计数：被 revert 链引用的快照不轮转；跨轴组合恢复时 full_pair 双方一起计数。

## 8. 配置子系统

网络策略（allow_domains/allow_cidr/deny_patterns，默认 deny 内网）
命令策略（默认不拦任何命令——能力释放）
限额定义（cpu/memory/pids/带宽——保活语义）
时限（任务最大时长）
全部走版本化恢复通道

## 9. 凭证子系统

inject：读取 secret 源 → 注入环境 → 记录元数据（值脱敏）
recycle：清除环境变量 → 撤销 token → 状态 recycled
快照交互：snapshot 前自动 detach 凭证；恢复后重新 inject
边界：credential_read 结构上不存在

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
| docker | shell/files/snapshot | commit / run from tag | 容器（共享内核） | 默认：开发/执行，秒级启停 |
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

审计事件流 + metrics（docker stats 时间序列）+ WebSocket 遥测推送（状态变更通知）

计量预留：核心只负责发计量事件（`audit.Event.Metering` 载荷 + `audit.Bus` 扇出），
计费/账户是事件流的下游消费者，不属于开源核心（见 §14）。计量单位与定价属商业策略，见内部文档。


## 13. 部署形态

单进程 MCP server（stdio/HTTP）+ sandbox.yaml 配置 + SQLite（审计/注册表）+ 文件系统（profile/snapshot 存储）

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

## 重构差距速览（v0.3.x → v3 架构）

| 现状 | 目标 | 差距 |
|---|---|---|
| Backend 无 capabilities | capabilities 声明 | 中 |
| audit 事件类型单一 | 12 类事件 + 脱敏 | 中 |
| 无资源模型抽象 | resource_id/type/元数据 | 大 |
| 无快照/回滚 | snapshot/revert + baseline | 大（P0） |
| safety 只有警告 | 策略模型（默认放行+显式收紧） | 中 |
| 无凭证子系统 | inject/recycle + 快照排除 | 大（P1） |
| 无 metrics | 指标采样 | 中（P1） |
| auth 单一 token | 三层身份（tenant/task/session） | 中（P2） |