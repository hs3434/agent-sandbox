# 架构文档 — agent-sandbox

> 状态: Accepted（brownfield：spec-first 项目，docs/design-spec-v3.md v3.23 为完整设计真相源）
> 规模: M（6 契约包 + 编排装配层，单仓库单进程）
> 本文档 = 方法论结构层（I1-I7 合规声明 + 模块映射 + 追溯表）；**语义设计以 spec 为准**，
> 两文档冲突时以 spec §16 AD-NN 为准，本档 changelog 记录对齐动作。

| Version | Date | Change |
|---------|------|--------|
| draft | 2026-11-25 | brownfield entry——从 spec v3.23 + pkg/ 现状逆向对齐，未经实现验证 |
| 1.0 | 2026-11-25 | P0-m3a 开工前定稿：分层声明、模块契约索引、映射表、NFR 追溯 |

---

## 1. Architecture Overview

单进程 MCP server（sandboxd）= 控制面 + 编排层 + 数据面路由。分层（I7，TCP/IP 式逻辑分层）：

```
MCP 工具层（17 工具，Agent 唯一可见面）
    ↓ 只依赖编排契约
编排层（生命周期状态机 + 并发仲裁 + 注册表 + 记忆 + 感知聚合）
    ↓ 只依赖数据面契约
数据面（backend 契约 → docker / ssh 实现；OS 差异内部化于实现内）
```

平铺豁免声明：P0 规模下逻辑三层映射到 Go package 为**扁平两层**——`pkg/`（契约+实现）
与 `cmd/sandboxd`（组合根）。声明"flat, no directory layering"，依赖纪律照常执行
（I5/I7 的单向依赖由 import 检查保证，不靠目录深度）。

## 2. Module Design（模块 = Go package）

| 模块 | 契约面 | 职责 | 阶段 |
|---|---|---|---|
| `pkg/resource` | Meta/Spec/Status/ValidateTransition/Orchestrator | 资源模型 + 状态机 + 仲裁锁 | ✅ m1/m2 |
| `pkg/backend` | Backend 接口 + Registry + ErrUnsupported | 数据面契约 + capability 门 | ✅ m1 |
| `pkg/audit` | Event/Sink/Bus/Query | 审计真相流（治理层） | ✅ m2 |
| `pkg/store` | Store（resources/handles 两表） | name 键控注册表 + 句柄账本持久化 | ✅ m2 |
| `pkg/snapshot` | 契约骨架 | P1 workdir 快照轴 | 骨架已置 |
| `pkg/policy` | 契约骨架 | P2 策略 | 骨架已置 |
| `pkg/container`（m3a 新） | Backend 实现（docker） | container 一生 + exec 双模式 + file + build | **m3a** |
| `pkg/remote`（m3b 新） | Backend 实现（ssh 瘦模式） | ControlMaster 自愈 + SFTP + 编码层 | m3b |
| `internal/orchestrator`（m3a 新） | 编排服务（create/reconcile/自愈） | 组合状态机+锁+store+backend | **m3a** |
| `internal/mcpserver`（m4 新） | MCP 工具面装配 | 17 工具 → 编排调用 | m4 |
| `cmd/sandboxd` | main | 唯一组合根（I3） | m4 收口 |

## 3. 七不变量合规（I1–I7）

- **I1 单一契约面**：每 package 一个入口文件（包 doc + 类型声明）；backend 实现
  只经 `backend.Backend` 接口被编排层消费
- **I2 契约 = 声明 + 行为规格**：接口方法注释承载错误语义；错误 = typed error
  （ErrUnsupported/ErrExists/ErrConflict/ErrInvalidTransition）——调用方可枚举
- **I3 组合根唯一**：`cmd/sandboxd/main.go` 唯一知道具体 backend；编排层只见接口
- **I4 数据单一写者**：SQLite 三表各有唯一写者——resources/handles 只归 `pkg/store`，
  events 只归 `pkg/audit`；编排层经 store 契约写，不裸 SQL
- **I5 依赖单向**：`resource ← store/audit/backend ← （m3a）container ← internal/* ← cmd`；
  禁环；反向通知经 audit.Bus（从上注册）
- **I6 声明面与覆盖链**：超时/上限类参数（exec timeout 60s / 探活 5s / 会话上限 4 /
  starting 30s / stopping 60s / 捎带积压 N）住各模块 `DEFAULT_*` 常量声明面，
  sandbox.yaml 用户可配层覆盖（m4 装配时接），注释与常量一致
- **I7 分层**：见 §1——工具层不摸 store 裸 SQL，编排层不摸 docker SDK；
  跨层传自描述数据（Meta/Event/Handle），不传活连接

## 4. NFR → 架构措施（spec §15 全 10 条）

| NFR | 措施 | 落点 |
|---|---|---|
| NFR-1 自愈 p95<5s | 镜像缓存命中路径：无快照流程，create 直达 | orchestrator+container |
| NFR-2 自愈 >99% | 失败重试一次 → error 态兜底，workdir 结构性不可销毁 | orchestrator |
| NFR-3 放行 100% | 命令策略 P0 默认空 | policy 骨架 |
| NFR-4 update=0 | 无 update 工具/接口；改镜像=同名重建 | 工具面+契约 |
| NFR-5 明文=0 | audit sink 指纹化（已实现） | pkg/audit |
| NFR-6 credential_read=0 | 工具面无此工具（结构性） | m4 面审计 |
| NFR-7 回溯 100% | 全操作过 audit.Bus；events 窄化读 | pkg/audit |
| NFR-8 无饥饿 | P2（配额执行器随治理身份） | — |
| NFR-9 冷启动 p95<10s | 镜像缓存 + 无预启动钩子 | container |
| NFR-10 exec p95<500ms | 直连 docker exec / ControlMaster 复用 | container/remote |

## 5. 场景 → 模块链（P0 核心五场景）

| 场景 | 链 |
|---|---|
| 造身体 | mcpserver → orchestrator → store(注册) → container.Create → audit |
| 同名自愈 | mcpserver → orchestrator（锁临界区：kill 旧 → run 新，name 键控复活） |
| 跑命令（阻塞/异步） | mcpserver → orchestrator → container.Exec → 句柄账本 → 捎带 |
| 改声明换身体 | file_write → container_build → remove+同名 create |
| sandboxd 重启 | orchestrator.Reconcile（注册表×daemon 比对） |

## 6. Implementation Mapping（模块 → 目录，gate 时声明）

| 模块 | 目录 | 状态 |
|---|---|---|
| 资源模型/状态机 | `pkg/resource/` | ✅ |
| backend 契约 | `pkg/backend/` | ✅ |
| 审计 | `pkg/audit/` | ✅ |
| store | `pkg/store/` | ✅ |
| docker backend | `pkg/container/` | m3a |
| ssh backend | `pkg/remote/` | m3b |
| 编排 | `internal/orchestrator/` | m3a |
| MCP server | `internal/mcpserver/` | m4 |
| 组合根 | `cmd/sandboxd/` | m4 |

## 7. Verification Matrix

`design/3-verification/feature-test-matrix.md`（随 m3a 首个可运行 backend 建立；
U=契约测试 / P=管道集成，P0 无 UI/D/M 维度）。
