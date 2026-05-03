# OpenAI 95% 周限额保护调度机制审查报告

审查日期：2026-05-03

审查对象：

- 本地代码仓库：`/Users/markdonish/Documents/New project/sub2api-fork-codex`
- 本地提交：`0b548229df39eb1858ef4b9e7c0cc1b46cf439f2`
- 生产只读核对主机：`198.23.137.118`
- 生产代码路径：`/opt/relay/sub2api-fork`
- 生产 Compose 路径：`/opt/relay/ai-relay-infra/sub2api`

## 1. 总结论

结论分两层：

1. 本地代码层面：95% 周限额保护机制已经接入调度根链路。
2. 生产网站层面：尚未配置完成，因为生产代码和运行镜像没有包含本次提交。

因此，不能说“现在网站已经从根本上生效”。准确说法是：

> 代码已经实现并推送，但生产环境还没有部署这套 95% 保护逻辑；线上当前不具备自动把 95% OpenAI OAuth 账号踢出调度的新增机制。

## 2. 本地代码链路审查

### 2.1 触发条件

实现文件：

- `backend/internal/service/openai_quota_guard.go`

关键配置：

```go
OpenAIQuotaGuardThresholdPercent = 95.0
OpenAIQuotaGuardReasonPrefix     = "hfc-quota-guard:"
OpenAIQuotaGuardReasonCodex7d    = "hfc-quota-guard:openai-codex-7d>=95"
OpenAIQuotaGuardReasonNoReset    = "hfc-quota-guard:openai-codex-7d>=95:no-reset-at"
OpenAIQuotaGuardNoResetCooldown  = 6 * time.Hour
```

判断规则：

- 只对 `platform=openai` 且 `type=oauth` 的账号生效。
- 读取 `extra.codex_7d_used_percent`。
- 当 `codex_7d_used_percent >= 95` 时触发保护。
- 如果存在未来的 `codex_7d_reset_at`，保护到该时间。
- 如果缺少 reset 时间，则保护 6 小时。
- 如果 reset 时间已经过去，则不继续保护。

### 2.2 调度根入口

实现文件：

- `backend/internal/service/account.go`

`Account.IsSchedulable()` 已接入：

```go
if a.IsOpenAIQuotaGuardedAt(now) {
    return false
}
```

这点很关键。因为 OpenAI 智能路由、sticky session、候选池过滤、最终 DB 重读校验，都不是单独判断“95%”字段，而是复用 `IsSchedulable()`。

审查到的关键调用点：

- `backend/internal/service/openai_account_scheduler.go`
  - sticky session 命中后会检查 `account.IsSchedulable()`
  - 候选池构建时会检查 `account.IsSchedulable()`
- `backend/internal/service/openai_gateway_service.go`
  - `isOpenAIAccountEligibleForRequest()` 使用 `!account.IsSchedulable()`
  - `recheckSelectedOpenAIAccountFromDB()` 会从 DB 重读账号后再次走 eligibility 检查
- `backend/internal/service/openai_ws_forwarder.go`
  - WebSocket sticky session 清理也走 `account.IsSchedulable()`

结论：本地代码不是只改了某一条路由，而是接到了账号可调度性的根判断。

### 2.3 数据来源

本地代码会从以下来源写入 `codex_7d_used_percent` 与 reset 时间：

1. OpenAI 正常响应头
   - `backend/internal/service/openai_gateway_service.go`
   - `backend/internal/service/openai_gateway_chat_completions.go`
   - `backend/internal/service/openai_gateway_messages.go`
   - `backend/internal/handler/openai_gateway_handler.go`
   - `backend/internal/handler/openai_images.go`

2. OpenAI 429 响应头
   - `backend/internal/service/ratelimit_service.go`

3. OpenAI Codex 探测快照
   - `backend/internal/service/account_usage_service.go`

解析函数：

- `ParseCodexRateLimitHeaders()`
- `buildCodexUsageExtraUpdates()`

会解析：

- `x-codex-primary-used-percent`
- `x-codex-primary-reset-after-seconds`
- `x-codex-primary-window-minutes`
- `x-codex-secondary-used-percent`
- `x-codex-secondary-reset-after-seconds`
- `x-codex-secondary-window-minutes`

并归一化到：

- `codex_5h_used_percent`
- `codex_5h_reset_at`
- `codex_7d_used_percent`
- `codex_7d_reset_at`

### 2.4 写入临时不可调度

实现文件：

- `backend/internal/service/openai_quota_guard.go`
- `backend/internal/repository/account_repo.go`

触发保护后调用：

```go
repo.SetTempUnschedulable(ctx, accountID, decision.Until, decision.Reason)
```

仓库层会写：

- `temp_unschedulable_until`
- `temp_unschedulable_reason`
- `updated_at`

并且会：

- 写入 `scheduler_outbox` 的 `account_changed` 事件
- 同步 scheduler 单账号快照

结论：本地代码层面，95% 触发后不只是更新 `extra`，也会主动让调度快照失效/刷新。

### 2.5 自动恢复

恢复不是把 `schedulable` 开关重新打开，而是依赖时间窗口自然过期：

- `temp_unschedulable_until <= NOW()` 后，SQL 候选池不再过滤它。
- `codex_7d_reset_at` 过去后，`IsOpenAIQuotaGuardedAt()` 也会返回 false。
- 没有 reset 时间的 fallback 保护，6 小时后过期。

结论：本地代码层面符合“不要永久关闭账号，只是在接近周限额时跳过，恢复后重新进入调度”的目标。

## 3. 本地验证结果

执行命令：

```bash
docker run --rm -v "$PWD":/src -w /src/backend golang:1.26.2 go test -tags unit ./internal/service -count=1
```

结果：

```text
ok  	github.com/Wei-Shaw/sub2api/internal/service	84.815s
```

已覆盖的关键单测：

- `TestOpenAIQuotaGuardDecisionFromExtra`
- `TestAccountIsSchedulable_OpenAIQuotaGuard`
- `TestApplyOpenAIQuotaGuardFromUpdates`
- `TestApplyOpenAIQuotaGuardFromUpdatesBelowThreshold`

测试覆盖内容：

- 94.9% 不保护。
- 95.0% 且 reset 时间在未来时保护。
- reset 时间过期后不保护。
- 缺少 reset 时间时用 6 小时 fallback。
- OpenAI API Key 不受 OAuth 周限额保护逻辑影响。
- 触发保护时会调用 `SetTempUnschedulable()`。

## 4. 生产只读核对结果

### 4.1 生产代码未包含本次保护文件

生产路径：

```text
/opt/relay/sub2api-fork
```

只读检查结果：

```text
BRANCH=feat/loadfactor-weighting
HEAD=da441ee2a31da8f742f3758b535e9062510951a6
QUOTA_GUARD_FILE=missing
```

本地已推送实现提交：

```text
0b548229df39eb1858ef4b9e7c0cc1b46cf439f2
```

结论：

> 生产代码 checkout 不是本次提交，且没有 `backend/internal/service/openai_quota_guard.go`。生产代码未包含 95% 保护实现。

### 4.2 生产运行镜像不是本次提交构建

生产 Compose 当前镜像：

```text
hfc/sub2api:model-picker-billing-e3c7b380-20260503-131115
```

运行容器：

```text
CONTAINER=sub2api IMAGE=hfc/sub2api:model-picker-billing-e3c7b380-20260503-131115 STATUS=Up 37 minutes (healthy)
```

结论：

> 生产运行镜像不是 `0b548229` 对应构建，线上容器不具备这次新增的 95% 保护逻辑。

### 4.3 生产数据库当前账号状态

只读聚合检查结果：

```text
OpenAI OAuth total: 9
OpenAI OAuth with codex_7d_used_percent: 9
Max numeric codex_7d_used_percent: 100
Any status >=95: 1
Any status >=95 and temp guarded: 0
Active + schedulable >=95: 0
HFC quota guard reason count: 0
```

进一步分组结果：

```text
status=active
schedulable=false
currently_rate_limited=true
currently_temp_unschedulable=null/false
temp_reason=<null>
count=1
```

解释：

- 当前确实存在一个 OpenAI OAuth 账号的周限额数值达到 100%。
- 这个账号当前不是 `schedulable=true`，并且还处在 `rate_limit_reset_at > NOW()` 的限流窗口里。
- 当前数据库没有任何 `hfc-quota-guard:%` 的临时不可调度记录。

结论：

> 线上现在没有出现“active + schedulable + >=95%”的账号，所以当前这一刻没有正在被错误调度的 95% 账号。但这不是因为新机制生效，而是因为该账号本来就已经 `schedulable=false` 或处在 rate limit 窗口。生产新增机制尚未上线。

## 5. 风险与缺口

### P0：生产未部署，线上不能认定完成

这是最大的缺口。

本地代码已推送，但生产代码和镜像未包含该提交。因此“网站机制已完成”这个说法目前不成立。

### P1：如果有人在生产上手动重新打开 100% 账号，当前线上代码不会执行新增 95% 保护

当前生产那个 100% 账号是：

- `schedulable=false`
- `rate_limit_reset_at > NOW()`

如果生产代码不部署新机制，后续有人手动把 `schedulable` 打开，并且 rate limit 窗口过期，旧线上代码不会因为 `codex_7d_used_percent >= 95` 自动跳过它。

### P1：缺少真实调度 E2E 测试

当前已有 service unit test，能证明判断逻辑和 `SetTempUnschedulable()` 调用。

但还没有一个更完整的 E2E 测试覆盖：

1. 构造 OpenAI OAuth 账号。
2. 写入 `codex_7d_used_percent=95` 和未来 reset。
3. 走 OpenAI 智能调度入口。
4. 证明该账号不会被选中。
5. reset 过期后证明可重新进入候选。

这不是当前代码链路的阻断问题，但上线前建议补。

### P2：`applyOpenAIQuotaGuardFromUpdates()` 的调用边界可以更硬

`Account.OpenAIQuotaGuardDecision()` 本身限制了 OpenAI OAuth。

但 `applyOpenAIQuotaGuardFromUpdates()` 只根据 updates 判断，没有接收完整 `Account` 来二次确认平台和类型。当前调用点基本都在 OpenAI OAuth/Codex header 场景里，实际误伤风险低；但从防御式设计看，最好把 account/type 校验也放到写入 temp unschedulable 之前。

## 6. 需要完成的上线动作

要让生产网站真正生效，需要完成以下动作：

1. 把提交 `0b548229df39eb1858ef4b9e7c0cc1b46cf439f2` 合入或 cherry-pick 到生产构建分支。
2. 在生产构建路径 `/opt/relay/sub2api-fork` 确认存在：
   - `backend/internal/service/openai_quota_guard.go`
   - `backend/internal/service/openai_quota_guard_test.go`
3. 构建新的 `hfc/sub2api:<new-tag>` 镜像。
4. 更新 `/opt/relay/ai-relay-infra/sub2api/docker-compose.yml` 的 `sub2api.image`。
5. 只重启 `sub2api` 服务，不动 Postgres/Redis。
6. 部署后做只读核对：
   - 容器镜像 tag 已更新。
   - 运行容器健康。
   - 代码中存在 `OpenAIQuotaGuardThresholdPercent = 95.0`。
   - 触发一次测试或等待真实 header 后，能看到 `temp_unschedulable_reason LIKE 'hfc-quota-guard:%'`。

## 7. 最终判定

| 项目 | 判定 | 说明 |
|---|---|---|
| 本地代码实现 | 通过 | 95% 判断、临时不可调度、调度过滤、自动恢复均已接入 |
| 本地测试 | 通过 | `go test -tags unit ./internal/service` 通过 |
| Git 推送 | 通过 | 提交 `0b548229` 已推送到 `codex/agent-platform-adapters` |
| 生产代码 | 未通过 | 生产 checkout 缺少 `openai_quota_guard.go` |
| 生产镜像 | 未通过 | 当前镜像不是本次提交构建 |
| 生产数据库状态 | 当前无立即误调度证据 | 100% 账号当前不是 schedulable=true，但这不是新机制生效 |
| 网站机制是否根本完成 | 未完成 | 必须部署后才算线上完成 |

## 8. 直接结论

这次复查后的真实结论是：

> 我上次完成的是“本地代码实现 + 提交 + 推送”，不是“生产网站已生效”。从生产只读检查看，线上没有包含这次 95% 周限额保护机制，所以如果你问的是当前网站线上调度机制是否已经从根本上配置完成，答案是：没有。

