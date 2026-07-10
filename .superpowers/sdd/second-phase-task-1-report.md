# 第二阶段 Task 1 报告：GPT-5.6 计费身份、前置定价与不可变证据

## 状态

实现完成，本轮代码与定向/整包测试通过。生产仍维持 **NO-GO**：本任务只解决请求进入上游前的可计价性、最终计费身份和价格快照证据，不包含钱包预占、成功上游响应后的可靠结算/outbox，也没有执行生产部署或镜像运行。

## 成功标准与实际结果

- 最终计费模型由 requested、compat、channel mapping、account mapping、upstream 的真实链路解析，GPT-5.6 禁止 requested billing、跨 tier/未知 tier 和非精确通配映射。
- Responses、Responses passthrough、Chat Completions（转换及 raw）、Messages 兼容入口和 WebSocket 在 token 获取/上游 transport 前完成定价 preflight；不可计价时返回类型化错误并保持上游零调用。
- 定价只解析一次并深拷贝为不可变 quote；结算使用该 quote，不重新读取已刷新的价格服务。
- LiteLLM revision 使用有效价格的 canonical SHA-256 派生，不信任 `PricingService.localHash`；区间顺序、指针地址、数据库 ID/时间戳不影响 hash，有效价格变化会改变 hash。
- WebSocket 每一 turn 重新解析 quote，连接建立时的早期 gate 不复用为结算价格。
- `usage_logs` expand-only 增加 nullable `pricing_source`、`pricing_revision`、`pricing_hash`；仅管理员 DTO 返回，普通用户 DTO 不暴露。
- 迁移 `172_add_usage_log_billing_model.sql` 未修改；新增 173 无回填、无默认值、无 NOT NULL、无索引。

## TDD 证据

### RED

首次运行：

```text
/Users/markdonish/.local/share/go/go1.26.3/bin/go test ./internal/service -run 'OpenAIBillingIdentity|ResolvePricingQuote|PricingEvidenceHash|ForwardWithOptions' -count=1
```

编译按预期失败，缺失的目标符号包括：

```text
undefined: OpenAIBillingIdentityInput
undefined: ResolveOpenAIBillingIdentity
undefined: ErrOpenAIBillingPreflight
undefined: ResolveQuote
```

环境补充：PATH 中没有 `go`（`zsh: command not found: go`），后续统一使用本机 Go 1.26.3 绝对路径。

### GREEN

```text
go test -tags=unit ./internal/service -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 96.374s

go test ./internal/service -run 'OpenAIBillingIdentity|ResolvePricingQuote|PricingEvidenceHash|ForwardWithOptions|OpenAISharedHTTPDispatch|OpenAIWSBilling|ImmutablePreflightQuote|RecordUsageUsesImmutablePreflightQuoteAndEvidence' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 2.304s

go test -tags=unit ./internal/repository -count=1
ok github.com/Wei-Shaw/sub2api/internal/repository 2.319s

go test ./internal/handler -run 'OpenAI|Responses|Messages|WebSocket|Chat' -count=1
ok github.com/Wei-Shaw/sub2api/internal/handler 1.831s

go test ./internal/handler/dto ./ent/... ./migrations -count=1
PASS
```

另有全量无 tag 的 `internal/service` 已通过（44.711s）。独立审查修复 WS 首轮 quote、渠道 exact TOCTOU、图片证据错配和 WS quote map 并发访问后，再次运行相关 service/handler 定向回归并通过。`internal/handler` 全包仍有与本任务无关的既有 macOS `/private/var` 与 `/var` 路径断言失败 `TestResolvePageImagePath`；本轮相关 handler 测试已单独通过。

## 数据库与安全审查

- 173 迁移使用三个 `ADD COLUMN IF NOT EXISTS`，均 nullable；历史行保持 NULL，回滚可删除新增列但本轮不提供 destructive down。
- PostgreSQL integration 已在隔离的 Testcontainers `postgres:18.1-alpine3.23` 与 `redis:8.4-alpine` 上执行：`TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate`、`TestUsageLogPricingEvidenceMigration173IsIdempotentAndLeavesHistoricalRowsNull` 均通过；repository 包结果 `ok ... 5.248s`。测试容器由 harness 创建并回收，未连接生产数据库。
- `git diff --check` 通过；迁移 172 的 `git diff --exit-code` 通过。
- 仓库 `make secret-scan` 失败，原因是 Makefile 引用的 `tools/secret_scan.py` 不存在；替代执行了本轮 diff 的常见 token、Bearer、AWS key、private key 和敏感文件名扫描，未命中。
- 定价证据只保存 source/revision/hash，不保存请求体、token、Authorization、API key 或价格原文。
- 独立代码审查和安全复审最终均无阻断项；账务 TOCTOU、图片证据错配、WS 首轮冻结及并发 map 问题均已修复并回归。
- 禁止文件 `reports/HFC_PRODUCTION_503_TRIAGE_2026-07-10.md` 未读取、未修改、未暂存。

## 残余风险

1. 本任务不解决“上游成功但 worker/进程在持久化前失败”的扣费可靠性；这是第二阶段 Task 2 的范围，生产保持 NO-GO。
2. 真实 PostgreSQL 迁移幂等/历史 NULL 已用隔离 Testcontainers 验证；生产迁移 rehearsal 仍未执行，继续保持 NO-GO。
3. 没有执行生产配置修改、镜像运行、部署、推送或 canary。

## 提交

实现提交：`66ad9913` (`feat: add openai billing preflight evidence`)

## 2026-07-10 Task 1 复审修复

### 修复范围

- Responses / passthrough 图片 intent 现在按请求中的最终 image tool model 与 size tier，在 token/HTTP/WS transport 前生成 image quote；不再用文本模型 quote 代替。
- 独立 `/v1/images/generations`、`/v1/images/edits` handler 启用同一 image preflight；错误返回 400 `invalid_request_error`，不会落到 502/503。
- API key 与 OAuth Images 的成功及部分成功结果都携带冻结 image identity/quote；`RecordUsage` 使用该 quote 的 image count、size tier 和 image multiplier 结算并保存 matching evidence，不再读取已变化的 live 价格。
- WS 首轮及后续 turn 都从该 turn payload 判断 image intent 并冻结对应 image identity；文本 identity 禁止覆盖 image result。
- channel image quote 必须命中本次请求的 exact size tier（或显式 default）；例如 4K 请求只有 1K 价格会在上游前拒绝，不能跨 tier 或按零价结算。
- Responses 图片 intent 同时冻结 image quote 与文本 fallback quote：`ImageCount > 0` 才消费 image quote；零图片输出使用文本 quote，避免 per-request 默认 1 导致过扣。独立 Images 零输出不会被强制按 1 张计费。
- token quote 必须至少有一项正数、有限的有效价格；空、全零、负数、NaN、Inf 的渠道价格 fail closed。非 GPT 正数渠道价格兼容性保留。
- image quote 明确记录实际生效来源：channel、group image、LiteLLM effective 或已知 `gpt-image-*` 的 built-in fallback；未知且无显式价格的图片模型 fail closed。

### 复审 RED

```text
go test -tags=unit ./internal/service -run 'ResolvePricingQuoteRejectsEmpty|ResolvePricingQuoteAllowsExplicitPositive|ChannelImageBillingUsesImageCountAndSharedMultiplier|ImagePricingPreflightRejectsBeforeUpstream' -count=1

FAIL TestResolvePricingQuoteRejectsEmptyExactChannelTokenPricing
An error is expected but got nil.

FAIL TestOpenAIGatewayServiceRecordUsage_ChannelImageBillingUsesImageCountAndSharedMultiplier
expected frozen quote total 0.75, actual live-price total 2.40.
```

### 复审 GREEN

```text
go test ./internal/service -run 'OpenAIBillingIdentity|ResolvePricingQuote|PricingEvidenceHash|ImagePricingPreflight|ForwardImages_|RecordUsage.*(ImmutablePreflightQuote|ChannelImageBilling)|OpenAIWSBilling|AttachOpenAIBillingIdentity' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 2.414s

go test ./internal/handler -run 'OpenAI|Responses|Images|Messages|Chat|WebSocket|PricingPreflightErrorsReturn400|UnpriceableImageCloses|ImageTurnPersists' -count=1
ok github.com/Wei-Shaw/sub2api/internal/handler 0.951s

go test -tags=unit ./internal/repository -count=1
ok github.com/Wei-Shaw/sub2api/internal/repository 2.909s
```

新增真实边界测试包括：HTTP handler preflight 返回 400 且 upstream hit=0；WS handler 对不可计价 image turn 关闭 1008 且 upstream dial=0；WS image turn 最终 usage 的 billing model/quote/evidence 一致；独立 Images 在 token/transport 前拒绝非法价格。

最终复审修复后再次运行：

```text
go test ./internal/service -run 'OpenAIBillingIdentity|ResolvePricingQuote|PricingEvidenceHash|ImagePricingPreflight|ForwardImages_|RecordUsage.*(ImmutablePreflightQuote|ChannelImageBilling|ImageIntentWithoutOutput)|OpenAIWSBilling|AttachOpenAIBillingIdentity|MissingRequestedSizeTier' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 1.374s

go test ./internal/handler -run 'OpenAI|Responses|Images|Messages|Chat|WebSocket|PricingPreflightErrorsReturn400|UnpriceableImageCloses|ImageTurnPersists' -count=1
ok github.com/Wei-Shaw/sub2api/internal/handler 0.800s

go test -tags=unit ./internal/repository -count=1
ok github.com/Wei-Shaw/sub2api/internal/repository 2.013s
```

`git diff --check`、迁移 172 不变检查、diff 级常见凭证扫描均通过。私有 503 报告继续保持未读取、未修改、未暂存。

修复提交标题：`fix: close image billing preflight gaps`（本报告与修复代码同一提交）。

## 2026-07-10 Task 1 最终零图与零价语义修复

### 最终复审范围

- API key 与 OAuth Images 的权威上游零输出均保持 `ImageCount=0`，不再回退请求参数 `n`；dedicated Images 零输出 settlement 成本为 0。
- API-key SSE 只有看到 `[DONE]`、`image_generation.completed`、`image_edit.completed` 或 `response.completed` 才视为完整；EOF/断线且无 terminal 属于结果未知并 fail closed。完整 terminal 的零图仍按权威 0 处理。
- 非 GPT token interval 允许未使用维度显式为 0，但每个可能命中的 interval 自身至少要有一项正有限价格；全零、负数、NaN、Inf interval 均在上游前拒绝，不能借用 base 或另一区间的正价通过。
- settlement 按实际使用维度再次 fail closed；被使用的 input/output/cache/image 维度没有正价时返回错误。

### RED 证据

```text
go test ./internal/service -run 'UpstreamZeroImagesDoesNotUseRequestedCount|DedicatedImageWithoutOutputChargesZero' -count=1
FAIL API-key expected 0 images, got requested n=3.
FAIL OAuth zero-output returned upstream did not return image output.

go test -tags=unit ./internal/service -run 'AllowsZeroUnusedIntervalDimension' -count=1
FAIL openai pricing unavailable for custom-zero-input.

go test ./internal/service -run 'EOFWithoutTerminalIsUnknown' -count=1
FAIL expected incomplete stream error, got nil.

go test -tags=unit ./internal/service -run 'RejectsAllZeroIntervalEvenWithUsableBasePrice' -count=1
FAIL expected ErrOpenAIPricingUnavailable, got nil.
```

### GREEN 与独立复审

```text
go test ./internal/service -run 'EOFWithoutTerminalIsUnknown|TerminalZeroImagesIsAuthoritative|UpstreamZeroImagesDoesNotUseRequestedCount|DedicatedImageWithoutOutputChargesZero' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 0.875s

go test -tags=unit ./internal/service -run 'AllowsZeroUnusedIntervalDimension|RejectsAllZeroOrInvalidIntervalPrices|RejectsAllZeroIntervalEvenWithUsableBasePrice|ValidateTokenPricingForUsage' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 1.024s

go test ./internal/service -run 'ForwardImages|RecordUsage|OpenAIBillingIdentity|ResolvePricingQuote|CalculateCostUnified' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 0.854s

go test -tags=unit ./internal/service -run 'ResolvePricingQuote|CalculateCostUnified' -count=1
ok github.com/Wei-Shaw/sub2api/internal/service 1.551s

go test ./internal/handler -run 'OpenAI|Responses|Images|Messages|Chat|WebSocket' -count=1
ok github.com/Wei-Shaw/sub2api/internal/handler 0.951s
```

最终独立代码复审：PASS。最终独立安全复审：PASS。`git diff --check` 通过；迁移 172 未修改；私有 503 报告未读取、未修改、未暂存。

合并状态 `0100cc4c` 的任务级最终复审：PASS。复审定向测试为 service unit `1.428s`、service `0.884s`、handler `0.792s`；Task 1 阻断项全部关闭。

修复提交标题：`fix: preserve zero-image and zero-tier billing semantics`。
