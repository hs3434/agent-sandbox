# 需求概览 — agent-sandbox

> 状态: Accepted（brownfield：需求真相源 = docs/design-spec-v3.md §0-§2/§15）
> 本档 = 方法论结构层（角色/场景/MoSCoW/验收）；语义以 spec 为准。

## 核心概念（术语表）

| 概念 | 定义 |
|---|---|
| 身体（exec_env） | 可存在、可操作、可自愈的执行环境（P0 = docker 容器 / ssh 机器） |
| 手（capability） | 平台保证的操作原语（shell/files/snapshot），非软件清单 |
| workdir 不变量 | `workdir = f(work_home, env_name)`——名字即地址，自愈机制基础 |
| 自愈 | remove + 同名 create；workdir 不变量 + name 键控兑现 |
| machine | sandbox.yaml 声明的执行目标（本机 docker / 远程 ssh 主机） |
| 捎带 | 工具响应附带滞留身体事件的感知通道 |

## 角色（含机器 actor）

- **Agent（机器 actor，主用户）**：经 MCP 17 工具操作身体
- **开发者（人）**：写 sandbox.yaml 声明 machine/配置；排查 error 态
- **sandboxd（服务）**：控制面进程，对账/仲裁/审计

## 用户故事与验收（P0 Must）

1. **As Agent**, I want `container_create(name)` so that I get a persistent body.
   Given 无同名记录, when create, then 容器 running 且 workdir 派生自名字（<10s, NFR-9）
2. **As Agent**, I want same-name recreate so that I self-heal a broken env without
   losing work. Given env error + workdir 有文件, when remove+同名 create,
   then workdir 原样可见, 审计标 resource_recreated（<5s, NFR-1）
3. **As Agent**, I want blocking shell_exec so that I get deterministic results.
   Given running env, when exec `echo hi`, then 返回 exit_code/output ≤60s 超时杀组
4. **As Agent**, I want async exec so that long tasks outlive the call.
   Given async=true, when exec, then 立即返回 handle；死亡经捎带/status 可见
5. **As Agent**, I want file_* so that I move structured data without shell quoting.
   Given workdir 文件, when read/write/patch/search, then 原子写 + 归一化 + 敏感路径警告
6. **As Agent**, I want state_get/state_put so that I remember across wakes.
   Given 单文档记忆, when wake 后 get, then 拿到上次写的文档（不随身体灭失）
7. **As sandboxd**, I want startup reconcile so that daemon-side containers are re-adopted.
   Given 控制面重启 + 容器存活, when 启动, then 注册表×daemon 对账收编，孤儿列出不删
8. **As 开发者**, I want declarative build so that image state is reproducible.
   Given workdir Dockerfile, when container_build(tag), then 镜像可作 create 基线

## Should（P0 尽力）/ Could / Won't（out of scope）

- Should: image_list 选型清单；events 增量回放；status 全景含 machines
- Could: ssh×windows 瘦模式（m3b）；sudo 管道注记（description 承载，零代码）
- **Won't（P0 明确不做）**: 策略执行 / 快照 / 凭证子系统 / metrics / vm/k8s /
  share 共享目录 / admin god-mode / sleep-wake 工具 / write_stdin 三件套 / MCP 推送

## 约束（来源）

- Go 1.24 + 纯 Go SQLite（无 CGO，来源：部署简单性，用户约束）
- AGPL 开源核心；business/ 不入仓（来源：open-core 决策 AD-005）
- MCP stdio/unix socket/HTTP（来源：协议生态）
- 不做拦截式安全（来源：AD-001 能力释放哲学）
