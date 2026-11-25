# Feature-Test Matrix — agent-sandbox P0

> 规则: 每场景/NFR 一行；coverage 格填测试名或 `MANUAL: 理由`；级别 U=契约测试 P=管道集成。
> NFR 行数必须 = 需求 NFR 数（闭环）。随实现逐格填充——空格 = 未验证，不是不存在。

## 场景矩阵

| # | 场景（需求档） | 级别 | 测试 | 状态 |
|---|---|---|---|---|
| 1 | container_create 造身体 ≤10s | U+P | container_test.TestCreateAndRemove（管道实测 1.7s） | ✅ |
| 2 | 同名自愈 workdir 保留 | U | orchestrator_test.TestSelfHealSameNameRecreate（审计含 resource_recreated） | ✅ |
| 3 | shell_exec 阻塞返回/超时杀组 | P | TestBlockingExec + TestBlockingExecTimeoutKillsProcessGroup | ✅ |
| 4 | async 立即返回 + 内核收尸 | P | TestAsyncExecHandleLedger（setsid 容器内 pid + 500ms 探活收尸） | ✅ |
| 5 | file 原子写/编码归一化 | P | TestFileOperations（CRLF→LF 归一化验证；敏感路径警告随 m4 description 承载） | ✅ |
| 6 | 记忆跨唤醒存续 | U | store/state 测试（m2 已覆盖存储半） | m4 |
| 7 | 重启对账收编/标 error/孤儿收编 | U | orchestrator_test.TestReconcile*（幂等二次 no-op 验证） | ✅ |
| 8 | container_build 路径约束/镜像可作基线 | P | TestContainerBuildAndRecreate（../escape 拒绝验证） | ✅ |
| 9 | 注册表 name 键控/重复拒绝 | U | store 测试（已绿） | ✅ |
| 10 | 状态机迁移表/幂等/仲裁锁 | U | lifecycle 测试（已绿） | ✅ |
| 11 | 审计 append/query/指纹化 | U | sqlite 测试（已绿） | ✅ |

## NFR 矩阵（spec §15 十条闭环）

| NFR | 目标 | 级别 | 测试/证据 | 状态 |
|---|---|---|---|---|
| NFR-1 自愈延迟 | <5s | U | TestSelfHealSameNameRecreate（fake 路径逻辑验证；真实镜像缓存计时随 m4 基准） | ✅ 逻辑 |
| NFR-2 自愈 >99% | >99% | U | TestCreateFailureMarksError（重试一次→无僵尸记录） | ✅ 逻辑 |
| NFR-3 放行率 100% | =100% | U | policy P0 默认空（规则面 P1，结构存在） | ✅ 结构 |
| NFR-4 update 接口 =0 | =0 | U | 面 audit（工具/接口枚举断言） | m4 |
| NFR-5 明文密钥 =0 | =0 | U | sqlite 测试（已绿） | ✅ |
| NFR-6 credential_read =0 | =0 | U | 面 audit | m4 |
| NFR-7 回溯率 100% | =100% | U | query 覆盖测试（已绿） | ✅ |
| NFR-8 无饥饿 | P2 | — | MANUAL: 随配额执行器 | P2 |
| NFR-9 冷启动 p95<10s | <10s | P | TestCreateAndRemove 实测 1.7s（含拉镜像则另计） | ✅ |
| NFR-10 exec p95<500ms | <500ms | P | 待 m4 基准测试（docker exec 直连路径已定） | m4 |

## 未覆盖

- ssh 瘦模式全场景（m3b 落地时补矩阵行）
- MCP 工具面端到端（m4 落地时补）
