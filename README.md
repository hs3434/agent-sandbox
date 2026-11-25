# agent-sandbox

AI 操作现实的支点（Go）：给 Agent 一个**身体**（执行环境）和**手**（操作原语），
让 AI 像人一样在各平台开发、操作系统。可释放能力、可回滚状态、可审计行为。

> 定位：平台级重版。轻量个人工具见 [sandbox-env-mcp](https://github.com/hs3434/sandbox-env-mcp)（Python，开源传播版）。
> 两者共享设计哲学：`docs/design-spec-v3.md`（v3 参考架构）。

## 设计哲学

1. **第一性 = 赋能操作** — 给 AI 身体（执行环境）和手（操作原语），让 AI 像人一样操作现实
2. **能力释放 + 自愈兜底** — 不拦命令（拦截 = 废了手）；恢复优先为**防止自我崩溃**（自愈），非防外敌
3. **一切皆资源，治理统一，操作分化** — 资源模型统一；capability 是运行时保证的原语，不是软件清单；物理机诚实说不支持快照
4. **执行环境不可变** — 无 update，改配置 = 重建（与回滚天然一致）
5. **恢复按可重建成本分层** — 环境用标准镜像重建/快照；策略用版本化；秘密只走注入回收、永不落快照
6. **控制面/数据面分离** — Agent 只见资源模型，底层实现可插拔
7. **用例中立** — 沙箱是开发/执行环境的通用底座；爬虫、浏览器、数据库都是环境里的程序（userland），不是平台概念

## 架构

```
控制面（MCP 工具集 + 资源模型 + 审计/策略/快照注册表）
    ↓
编排层（生命周期状态机 / 恢复调度 / 任务归属 / 配额保活）
    ↓
数据面（backend: docker / ssh / k8s(future)）
```

## 目录结构

```
cmd/sandboxd/        # 组合根：配置装配 + orchestrator 适配 + MCP server
internal/config/     # sandbox.yaml（machines[kind=docker|ssh] / work_home / 超时）
internal/orchestrator/  # 控制面门面：write-ahead 创建 / 对账 / 会话注册表 / 状态机
internal/mcpserver/  # 17 工具 MCP 面（stdio），不 import orchestrator（adapter 缝）
pkg/resource/        # 资源模型：类型 / capability / 元数据 / 状态常量
pkg/backend/         # 数据面契约：Backend 接口 + 注册表 + ErrUnsupported
pkg/container/       # docker backend：生命周期 / 双模式 exec / 持久会话 / async reaper
pkg/remote/          # ssh backend（瘦模式）：阻塞 exec / SFTP / 持久会话 / ControlMaster 自愈
pkg/shell/           # 会话引擎：marker 分帧 / 方言（posix|powershell）/ rg→grep 搜索
pkg/store/           # SQLite：注册表（name 键控）+ 状态文档 + 句柄账本
pkg/audit/           # 审计：事件记录 + Sink 接口 + Bus（SQLite sink + 游标）
pkg/snapshot/        # 快照子系统：注册表记录 / scope（env 轴先行，§7.0）
pkg/policy/          # 策略资源：命令规则 + 默认放行决策（P1 扩展网络/配额）
docs/design-spec-v3.md  # 参考架构（P0 施工基线，§14 开源核心/平台层边界）
```

## Roadmap

- **P0 ✅**：资源模型 + 17 工具 MCP 面（container/shell/file/sense/memory 5 族）+ docker×linux + ssh×linux/windows（瘦模式）+ 持久会话 + 对账自愈 + 句柄账本闭环
- **P1**：伴生服务（远程 async/账本/断线五层）+ 策略资源化（网络边界）+ machine_connect（条件触发）+ 审计脱敏
- **P2**：三层身份（tenant/task/session）+ metrics + vm/k8s backend 评估

## Build

```bash
go build ./...
go test ./...
```

## License

AGPL-3.0（与 sandbox-env-mcp 一致）