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
- PostgreSQL integration 测试已补充幂等与历史 NULL 断言，但按任务边界没有启动 Docker/testcontainer 镜像，因此真实 PostgreSQL 执行证据为：`信息缺失`。
- `git diff --check` 通过；迁移 172 的 `git diff --exit-code` 通过。
- 仓库 `make secret-scan` 失败，原因是 Makefile 引用的 `tools/secret_scan.py` 不存在；替代执行了本轮 diff 的常见 token、Bearer、AWS key、private key 和敏感文件名扫描，未命中。
- 定价证据只保存 source/revision/hash，不保存请求体、token、Authorization、API key 或价格原文。
- 独立代码审查和安全复审最终均无阻断项；账务 TOCTOU、图片证据错配、WS 首轮冻结及并发 map 问题均已修复并回归。
- 禁止文件 `reports/HFC_PRODUCTION_503_TRIAGE_2026-07-10.md` 未读取、未修改、未暂存。

## 残余风险

1. 本任务不解决“上游成功但 worker/进程在持久化前失败”的扣费可靠性；这是第二阶段 Task 2 的范围，生产保持 NO-GO。
2. 真实 PostgreSQL 迁移幂等/历史 NULL 未在本轮启动容器验证。
3. 没有执行生产配置修改、镜像运行、部署、推送或 canary。

## 提交

实现提交：`66ad9913` (`feat: add openai billing preflight evidence`)
