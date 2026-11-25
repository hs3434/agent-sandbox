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
| 6 | 记忆跨唤醒存续 | U | mcpserver_test.TestStateDocRoundtrip（state_get/state_put 单文档往返） | ✅ |
| 7 | 重启对账：校准/标 error/他物不认领 | U | orchestrator_test.TestReconcileConfirmsOursAndMarksDead（daemon-only = unmanaged，幂等二次 no-op） | ✅ |
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
| NFR-4 update 接口 =0 | =0 | U | mcpserver_test.TestToolFaceComplete（17 工具枚举断言，无 update 面） | ✅ |
| NFR-5 明文密钥 =0 | =0 | U | sqlite 测试（已绿） | ✅ |
| NFR-6 credential_read =0 | =0 | U | mcpserver_test.TestFaceAuditNFR4And6（面枚举无 credential_read） | ✅ |
| NFR-7 回溯率 100% | =100% | U | query 覆盖测试（已绿） | ✅ |
| NFR-8 无饥饿 | P2 | — | MANUAL: 随配额执行器 | P2 |
| NFR-9 冷启动 p95<10s | <10s | P | TestCreateAndRemove 实测 1.7s（含拉镜像则另计） | ✅ |
| NFR-10 exec p95<500ms | <500ms | P | e2e 冒烟实测 exec 69~102ms（docker exec 直连路径，e2e/smoke.py） | ✅ 实测 |

## 追加场景（m3b ssh 瘦模式 + m4 工具面）

| # | 场景 | 级别 | 测试 | 状态 |
|---|---|---|---|---|
| 12 | ssh 连接 + 阻塞 exec | P | remote_test.TestConnectAndExec（loopback sshd @39222 实管道） | ✅ |
| 13 | ssh exec 超时强杀 | P | remote_test.TestExecTimeout（Cancel+WaitDelay 关管道，不挂死） | ✅ |
| 14 | ssh SFTP 读写 + 编码归一 | P | remote_test.TestSFTPReadWrite（原子 temp+PosixRename） | ✅ |
| 15 | ssh 连接自愈（僵尸往返探测） | P | remote_test.TestConnectionSelfHealing（nonce 往返挂死→强拆重建） | ✅ |
| 16 | ssh 会话上限 | U | remote_test.TestSessionCap（per-machine 上限 4） | ✅ |
| 17 | ssh 诚实能力面 | U | remote_test.TestHonestCapabilities（瘦模式面不越权宣称） | ✅ |
| 18 | MCP 面完整性 | U | mcpserver_test.TestToolFaceComplete（17 工具 = 5 族齐全） | ✅ |
| 19 | 面审计（NFR-4/6） | U | mcpserver_test.TestFaceAuditNFR4And6 | ✅ |
| 20 | 捎带事件游标（每事件一次） | U | mcpserver_test.TestPiggybackDeliversEachEventOnce | ✅ |
| 21 | 记忆单文档往返 | U | mcpserver_test.TestStateDocRoundtrip | ✅ |
| 22 | 事件游标查询 | U | mcpserver_test.TestEventsCursor | ✅ |
| 23 | 名字→ID 解析（名字是资产地址） | U | orchestrator_test.TestRemoveByName | ✅ |
| 25 | 创建写前意图（先落库再建容器） | U | orchestrator_test.TestCreateWritesIntentBeforeBody（镜像进库，status 可答） | ✅ |
| 26 | 失败创建留 error 行 | U | orchestrator_test.TestCreateFailureLeavesErrorRow（自愈两步法的既决条件） | ✅ |
| 27 | status 结构分层 + 网络地址 | U | orchestrator_test.TestStatusShapeAndContent（集合=按名 map，default=指针，env 行带 network{ip,dns}） | ✅ |
| 28 | ssh machine 真实状态 + 端点地址 | P | e2e：loop=connected / deadbox=disconnected，address=host:port（状态不写死） | ✅ |
| 29 | 浅查/深查分工 | U | orchestrator_test.TestStatusShapeAndContent（status 无 backend/created/id）+ TestInspectIsTheDeepView（inspect 出处齐全） | ✅ |
| 24 | 端到端 stdio 链路 | P | e2e/smoke.py（17 工具 + create→exec→file×3→status→remove） | ✅ |

## 追加场景（v3.30 持久会话 + 搜索引擎化）

| # | 场景 | 级别 | 测试 | 状态 |
|---|---|---|---|---|
| 30 | marker 分帧（Send 完成 + 退出码回传） | U | shell_test.TestSendBlockingCompletesOnMarker（fake shell，exit 7 透传） | ✅ |
| 31 | 超时是事实不是悬挂（124 + 会话存活） | U | shell_test.TestSendTimeoutKeepsSessionAlive（超时后 pending 清空、可复用） | ✅ |
| 32 | 方言命令形状（posix/powershell） | U | shell_test.TestDialectCommandShapes | ✅ |
| 33 | rg→grep 兜底 + 诊断分离 | U | shell_test.TestSearchFallsBackToGrepWhenRGMissing / TestSearchContentSeparatesDiagnostics 等 7 测 | ✅ |
| 34 | 会话状态跨调用持久（cd/export） | P | e2e/sess_full.py 步 2（docker：`cd /tmp && export` 后 pwd/echo 命中） | ✅ |
| 35 | ssh 目标会话 | P | e2e/sess_full.py 步 5（loop@39222：远端输出 + cd 持久，pkg 层 sshsess.go） | ✅ |
| 36 | 会话生命周期（new/list/remove） | P | e2e/sess_full.py 步 1/4/6/7 | ✅ |
| 37 | async 句柄账本闭环（write-ahead + 死亡 hook） | P | e2e/async_ledger.py（账本行 running→dead "exited (code 0)"） | ✅ |

## 未覆盖

- NFR-8 无饥饿：随配额执行器（P2）
- P1 能力（machine_connect/disconnect、镜像构建产物校验、远程伴生把账本/async 带去 ssh）
