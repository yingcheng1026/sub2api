# 第二阶段 Task 3：GPT-5.6 403 模型隔离与稳定错误边界

## 结论

- 状态：代码候选已实现并通过定向、默认包级和 unit 标签测试。
- 生产：`NO-GO`；未部署、未重启、未连接生产数据库或 Redis、未运行或推送候选镜像。
- 私有 503 原始记录：未读取、未修改、未暂存、未提交。

## RED 证据

实现前定向测试确认：

```text
RateLimitService.HandleUpstreamErrorForModel undefined

Responses: expected 403, actual 503
Chat Completions: expected 403, actual 503
Anthropic Messages compat: expected 403, actual 503
```

## 已实现

1. 新增 model-aware upstream error 入口。OpenAI exact GPT-5.6 tier 的 403 只调用 `SetModelRateLimit`，不递增账号级 403 counter，不调用 `SetError`，也不设置 account-wide temp unschedulable。
2. 模型隔离 key 使用账号 canonical tier 的 exact 映射目标；无 entitlement、跨 tier 或未知 GPT-5.6 名称不会进入新分支，继续沿用既有安全策略。
3. 模型隔离持久化失败时仍让当前请求 failover，但禁止降级成整账号禁用。
4. Responses、Chat Completions、Anthropic Messages、Images 和 passthrough 的 OpenAI error 路径均携带模型身份进入 model-aware policy。
5. 新增 typed `ErrNoAvailableOpenAIAccounts`。只有 exact GPT-5.6 的首次明确无账号，或上一跳明确为 GPT-5.6 403 时，才转换为 HTTP 403 `model_not_available`；数据库、调度器及已有 5xx/429 failover 保持原错误。
6. WebSocket 在相同无授权/未开放场景使用 1008 policy violation；原跨模型映射 1008 边界保持不变。
7. 403 运行日志不再输出 raw body，仅记录脱敏后的 upstream message 与响应字节数。

## GREEN 证据

Go 1.26.3 本地容器串行执行：

```text
go test -tags=unit ./internal/service -run 'TestRateLimitService_HandleUpstreamErrorForModel|TestRateLimitService_HandleUpstreamError_OpenAI403' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service

go test ./internal/handler -run 'TestOpenAIGPT56|TestOpenAIResponsesWebSocket_GPT56Unavailable' -count=1
ok github.com/Wei-Shaw/sub2api/internal/handler

go test ./internal/service -run 'Test(OpenAI|RateLimitService)' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service

go test ./internal/service ./internal/handler -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 46.828s
ok github.com/Wei-Shaw/sub2api/internal/handler 21.453s

go test -tags=unit ./internal/service -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 92.938s
```

额外回归覆盖：exact tier 无 entitlement 回退旧策略；mapped model-rate-limit key 能被调度器读取；上游 500 后账号耗尽仍返回 502 `upstream_error`，不误报 403。

## 独立审查

- 安全复审：`PASS`。确认 exact entitlement、模型级失败隔离和无 raw body 日志满足边界。
- 代码复审：`PASS`。确认首次无账号、上一跳 403 与已有 5xx/429 的错误分类顺序正确。

## 剩余边界

- 整仓、race、vet、前端、Claude Code harness、安全扫描和最终镜像仍属于后续验收。
- Windows 实机验证：`信息缺失`。
