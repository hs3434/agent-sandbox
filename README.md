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
cmd/sandboxd/        # 控制面入口（MCP server，P0-m2 接入）
pkg/resource/        # 资源模型：类型 / capability / 元数据 / 状态常量
pkg/backend/         # 数据面契约：Backend 接口 + 注册表 + ErrUnsupported
pkg/snapshot/        # 快照子系统：注册表记录 / scope（env 轴先行，§7.0）
pkg/policy/          # 策略资源：命令规则 + 默认放行决策（P1 扩展网络/配额）
pkg/audit/           # 审计：事件记录 + Sink 接口 + Bus（SQLite sink P0-m2）
docs/design-spec-v3.md  # 参考架构（P0 施工基线，§14 开源核心/平台层边界）
```

## Roadmap

- **P0**：资源模型 + capability + snapshot/revert（标准镜像重建优先 + commit 备选）+ 自动 baseline
- **P1**：策略资源化（网络边界）+ 凭证注入/回收 + 审计脱敏
- **P2**：三层身份（tenant/task/session）+ metrics

## Build

```bash
go build ./...
go test ./...
```

## License

AGPL-3.0（与 sandbox-env-mcp 一致）