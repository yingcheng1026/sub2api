# 月卡 F+ locked_rates 执行记录（2026-05-17）

## 来源

- 交接文件：`HANDOFF_MONTHLY_LOCKED_RATES_FOR_MARK_2026-05-17.md`
- 原交接引用的详细 plan：`docs/plans/2026-05-17-monthly-card-locked-rates-and-channel-status-design.md`
- 本地状态：上述详细 plan 未在当前仓库中找到，属于信息缺失；本记录按交接文件和当前代码实际结构落地。

## 执行范围

本次只做源码与迁移实现，未执行生产部署、未修改生产数据库、未触碰链动小铺商品。

落地内容：

- `user_subscriptions.locked_rates JSONB`
- 真实扣费倍率优先级：`locked_rates -> user_group_rate_multipliers -> groups.rate_multiplier -> default`
- 新月卡 F+ 自动按 plan 关联 group 生成锁定倍率：
  - GPT / Kiro：`1.0x`
  - Antigravity：`3.5x`
  - Claude：`8.5x`
- 老订阅 / 老用户迁移锁价：
  - `user_subscriptions.id IN (16, 29, 40, 45)`：`0.3x`
  - `user_id = 21`：`0.3x`
  - 迁移按当前 active F+ 相关 group 动态生成 `group_id -> 0.3`，避免生产 group id 与本地假设不一致导致漏锁。
- `/v1/usage` 返回 `rate_context` 和订阅 `locked_rates`
- 用户端显示锁定倍率：
  - API Key 分组 badge
  - 可用渠道页
  - 订阅钱包页 group 倍率列表

## 关键边界

- 没有修改 `plan_groups` 的语义。
- 没有新建 plan。
- 额度卡 `plan_type='credits'` 不生成月卡 F+ 锁定倍率，继续按原 group/user rate 规则。
- 锁定倍率只在对应订阅存在并覆盖当前调用 group 时生效。

## 验证

已执行：

```bash
docker run --rm -v sub2api-go-mod:/go/pkg/mod -v sub2api-go-build:/root/.cache/go-build -v "$PWD":/src -w /src/backend golang:1.26.3-alpine go test ./internal/service -run 'Test(UserSubscriptionLockedRateForGroup|ResolveEffectiveRateMultiplier_LockedRatesOverrideUserGroupRate|MonthlyLockedRateForGroup|OpenAIGatewayServiceRecordUsage_LockedRatesOverrideUserSpecificGroupRate)'
docker run --rm -v sub2api-go-mod:/go/pkg/mod -v sub2api-go-build:/root/.cache/go-build -v "$PWD":/src -w /src/backend golang:1.26.3-alpine go test ./internal/service ./internal/handler ./internal/server/middleware ./internal/repository -run 'Test(UserSubscriptionLockedRateForGroup|ResolveEffectiveRateMultiplier_LockedRatesOverrideUserGroupRate|MonthlyLockedRateForGroup|OpenAIGatewayServiceRecordUsage_LockedRatesOverrideUserSpecificGroupRate|___CompileOnly)'
docker run --rm -v sub2api-go-mod:/go/pkg/mod -v sub2api-go-build:/root/.cache/go-build -v "$PWD":/src -w /src/backend golang:1.26.3-alpine go test ./cmd/server -run '___CompileOnly'
cd frontend && pnpm typecheck
docker run --rm -v "$PWD":/src -v sub2api-frontend-node-modules:/src/frontend/node_modules -v sub2api-pnpm-store:/root/.local/share/pnpm/store -w /src/frontend node:22-alpine sh -lc "corepack enable && corepack prepare pnpm@10.33.2 --activate && pnpm rebuild esbuild vue-demi && pnpm build"
```

结果：

- 后端定向测试通过。
- 后端相关包编译通过。
- 前端 `vue-tsc --noEmit` 通过。
- 前端 Linux 容器完整 build 通过。

## 未执行

- 未在生产库执行 migration。
- 未构建/推送生产镜像。
- 未切线上流量。
