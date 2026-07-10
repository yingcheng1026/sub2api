# 第二阶段 Task 2B：OpenAI durable usage 生产接线验证

## 结论

- 状态：代码候选已实现并通过定向单元、包级和真实 PostgreSQL outbox 全场景测试。
- 生产：`NO-GO`，本任务未部署、未重启服务、未运行或推送镜像、未改生产数据。
- 私有 503 原始记录：未读取、未修改、未暂存、未提交。

## 已实现

1. OpenAI Responses、Chat Completions、Anthropic Messages 兼容、Images、WebSocket usage 同步写入数据库 outbox，不再进入可 drop/sample 的内存 usage 队列。
2. `UsageBillingEnvelope` 保存安全的 usage replay 快照：执行/请求/上游/计费模型、token、价格证据、费用、渠道映射和发生时间；不保存请求体、headers、IP、UA 或凭据。API Key 仅冻结 SHA-256 auth cache locator，quota effect 缺 locator 时拒绝入队。
3. replay 固定顺序为 `Apply -> usage_logs 幂等写入 -> 幂等缓存失效 -> Complete`。
4. 后台 worker 启动立即排空、支持 wake hint、轮询恢复、错误退避和优雅停止；服务 cleanup 先停止 worker，再关闭缓存与数据库。
5. 路由 group 与实际计费 group 分别冻结；月卡跨组只接受明确的 `subscription_plan_groups` 覆盖，钱包和原月卡扣费语义保持不变。
6. Claude alias 与原生 GPT 请求别名仅写入 `requested_model`，`model`、`upstream_model` 和 `billing_model` 使用实际执行 GPT。
7. quota replay 按冻结 locator 直接可靠清除认证缓存，不依赖 active key 枚举，因此 API Key 软删除/tombstone 后仍可重试。
8. WebSocket 每个连接/turn 使用稳定且互不冲突的账单 request ID。

## 验证证据

### 定向单元测试

```text
go test ./internal/service -run 'TestUsageBilling(Envelope|Replay)|TestOpenAIUsageBillingProducer' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service

go test -tags=unit ./internal/service -run 'TestAPIKeyService_InvalidateAuthCacheBy(Locator|UserIDReliable)' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service
```

### 真实 PostgreSQL

```text
go test -tags=integration ./internal/repository -run '^TestUsageBillingOutbox' -count=1 -v
PASS BalanceAckCrashReplayChargesAndLogsOnce
PASS MonthlyAndWalletPreserveFrozenSubscription/monthly
PASS MonthlyAndWalletPreserveFrozenSubscription/monthly_plan_coverage
PASS MonthlyAndWalletPreserveFrozenSubscription/wallet
PASS PendingEventSurvivesWorkerRestart
PASS EnqueueIdempotencyAndConflict
PASS ClaimLeaseAndStaleOwner
PASS RetryAndDeadLetterLifecycle
PASS EnqueueRejectsCrossTenantAndInvalidBillingMode
PASS FinalAttemptCanBeReclaimedWithFencingToken
PASS Migration_SchemaAndIndexes
```

验证了：进程退出前仅 enqueue、重启后恢复；ack 崩溃重放只扣一次且 usage log 一条；月卡日/周/月累计不变义；M:N 套餐覆盖与未覆盖拒绝；钱包余额和 ledger 不重复；locator 与 API Key 绑定、NULL `key_hash` fallback 和错误 locator fail closed。

### 包级与 Wire

```text
go generate ./cmd/server
wire: wrote cmd/server/wire_gen.go

go test -p=1 ./internal/service ./internal/handler ./cmd/server ./internal/repository -count=1
ok github.com/Wei-Shaw/sub2api/internal/service
ok github.com/Wei-Shaw/sub2api/internal/handler
ok github.com/Wei-Shaw/sub2api/cmd/server
ok github.com/Wei-Shaw/sub2api/internal/repository
```

本轮在 Go 1.26.3 容器中串行执行，避免 Docker Desktop 资源争用造成编译器被系统杀掉；真实 PostgreSQL 运行使用 Testcontainers，未连接生产数据库。

### 独立审查

- 代码复审：`PASS`。确认 legacy `UsageBillingCommand` 指纹保持兼容，locator 仅受 outbox envelope 指纹保护。
- 安全复审：`PASS`。确认 raw API Key 不落 outbox/日志，locator 绑定与软删除后可靠失效闭环成立。

## 剩余边界

- 整仓、race、vet、Claude Code harness、前端、迁移全量、安全扫描和最终镜像仍属于后续验收，未据此放开生产。
- 上游已成功执行后、outbox enqueue 恰逢数据库不可用时会显式返回并记录高优先级错误，不会回退到旧扣费或内存队列；跨上游与本地数据库无法形成同一事务，此边界继续作为 canary/监控前置风险保留。
- Windows 实机验证：`信息缺失`。
