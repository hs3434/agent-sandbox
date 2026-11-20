# agent-sandbox

Agent 执行环境的统一资源管理平台（Go）。为 AI Agent 提供**可释放能力、可回滚状态、可审计行为**的执行沙箱。

> 定位：平台级重版。轻量个人工具见 [sandbox-env-mcp](https://github.com/hs3434/sandbox-env-mcp)（Python，开源传播版）。
> 两者共享设计哲学：`docs/design-spec-v3.md`（v3 参考架构）。

## 设计哲学

1. **一切皆资源，治理统一，操作分化** — 资源模型（ID/标签/审计/权限）统一；操作按 capability 声明（capability = 运行时保证的原语，不是软件清单；物理机诚实说不支持快照）
2. **恢复优先（recovery-first）** — 不拦任何命令（能力最大化），安全靠"环境基线 + 随时回滚"（容器 CoW 重建、快照点、凭证重注入）
3. **执行环境不可变** — 无 update，改配置 = 重建（与回滚天然一致）
4. **恢复机制按可重建成本分层** — 高成本状态（环境）用快照；低成本状态（策略）用版本化；秘密（凭证）只走注入回收、永不落快照
5. **控制面/数据面分离** — Agent 只见资源模型，底层实现可插拔
6. **用例中立** — 沙箱是开发/执行环境的通用底座；爬虫、浏览器、数据库都是环境里的程序（userland），不是平台概念

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
cmd/sandboxd/        # 控制面入口（MCP server，P0-m2 接入）
pkg/resource/        # 资源模型：类型 / capability / 元数据 / 状态常量
pkg/backend/         # 数据面契约：Backend 接口 + 注册表 + ErrUnsupported
pkg/snapshot/        # 快照子系统：注册表记录 / scope（env 轴先行，§7.0）
pkg/policy/          # 策略资源：命令规则 + 默认放行决策（P1 扩展网络/配额）
pkg/audit/           # 审计：事件记录 + Sink 接口 + Bus（SQLite sink P0-m2）
docs/design-spec-v3.md  # 参考架构（P0 施工基线，§14 开源核心/平台层边界）
```

## Roadmap

- **P0**：资源模型 + capability + snapshot/revert（docker commit → run from tag）+ 自动 baseline
- **P1**：策略资源化（网络边界）+ 凭证注入/回收 + 审计脱敏
- **P2**：三层身份（tenant/task/session）+ metrics

## Build

```bash
go build ./...
go test ./...
```

## License

AGPL-3.0（与 sandbox-env-mcp 一致）