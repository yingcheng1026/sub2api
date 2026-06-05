# HFC Frontend Page / Sidebar Change Review - 2026-06-04

## Scope

本报告只梳理当前升级候选里的前端页面、个人主页侧边栏、用户可见入口变化，供上线前人工审核。

本轮没有生产部署、没有生产迁移、没有生产数据修改。

最高边界继续不变：页面可以展示、提醒、跳转，但不能改变 HFC 计费口径、钱包账本、月卡扣费、模型价格、倍率、支付链路、分组覆盖、LoadFactor 或调度优先级。

## Summary

结论先说清楚：

- 当前候选分支没有修改 `frontend/src/components/layout/AppSidebar.vue`。
- 当前候选分支没有修改 `frontend/src/router/index.ts`。
- 因此，本次候选没有新增“个人主页侧边栏菜单入口”。
- 用户侧前端变化主要是页面内容和弹窗变化，不是侧边栏结构变化。
- 相对 2026-06-03 safe-fusion 交接基线，用户侧只剩购买/订阅/续费页的 Opus 4.8 错误横幅删除；后台新增用户余额历史中的充值余额/订阅钱包拆分展示。
- 相对更早的生产保护基线 `6abdf8d7`，用户侧页面变化更明显，集中在 Dashboard、购买页、订阅页、兑换页、套餐卡、续费弹窗和 Opus 4.8 展示常量。

## Current Personal Sidebar

普通用户当前实际侧边栏来自 `buildSelfNavItems(true)`，菜单顺序如下：

| 菜单 | 路径 | 本次是否新增 | 显示条件 |
| --- | --- | --- | --- |
| 仪表盘 | `/dashboard` | 否 | 普通用户显示；管理员个人区不含这个，因为管理员有 `/admin/dashboard` |
| API 密钥 | `/keys` | 否 | 始终显示 |
| 使用记录 | `/usage` | 否 | simple mode 隐藏 |
| 可用渠道 | `/available-channels` | 否 | simple mode 隐藏；受 `availableChannels` feature flag 控制 |
| 渠道状态 | `/monitor` | 否 | 受 `channelMonitor` feature flag 控制 |
| 我的订阅 | `/subscriptions` | 否 | simple mode 隐藏 |
| 充值/订阅 | `/purchase` | 否 | simple mode 隐藏；受 `payment` feature flag 控制 |
| 我的订单 | `/orders` | 否 | simple mode 隐藏；受 `payment` feature flag 控制 |
| 兑换 | `/redeem` | 否 | simple mode 隐藏 |
| 邀请返利 | `/affiliate` | 否 | simple mode 隐藏；受 `affiliate` feature flag 控制 |
| 个人资料 | `/profile` | 否 | 始终显示 |
| 自定义菜单 | `/custom/:id` | 否 | 后台配置 `custom_menu_items` 且 visibility=user |

管理员侧边栏下方“我的账户”区复用同一组个人菜单，但不含 `/dashboard`。simple mode 下管理员的“我的账户”区会隐藏，管理区会额外保留 `/keys` 入口，避免管理员自己的 API key 管理入口消失。

## User Page Changes

### 1. 仪表盘 `/dashboard`

用户可见变化：

- 非 simple mode 且没有 wallet 订阅时，新增“当前余额”充值 CTA 卡片。
- CTA 显示当前余额、永久有效、所有模型可用、按渠道倍率扣费。
- 点击“立即充值 / 续费”不跳页面，打开统一的链动 SKU 弹窗。
- wallet 订阅用户继续显示 `WalletBalanceCard` 和 `WalletModelRouteList`；点击续费也打开同一个统一弹窗。

业务边界：

- 只是入口和展示变化。
- 不改余额数值来源。
- 不改扣费、账本、倍率或月卡逻辑。

### 2. 充值/订阅页 `/purchase`

用户可见变化：

- select 阶段顶部增加兑换码有效期提醒：
  - “购买后如获得兑换码，请在 30 天内完成兑换；未发放的库存码不计入有效期。”
- 通用余额充值说明增加“如订单发放兑换码，请在 30 天内兑换”。
- 客户侧错误的 Opus 4.8 月卡开放提示横幅已删除。

业务边界：

- 支付仍走 HFC 现有链动/支付逻辑。
- 不新增 Airwallex、多币种、邮件通知、OAuth 权益。
- 不改变套餐价格、月卡覆盖、钱包账本或扣费。

### 3. 我的订阅 `/subscriptions`

用户可见变化：

- 续费弹窗抽成共享组件 `RenewLiandongModal`，订阅页和 Dashboard 使用同一套弹窗。
- 钱包模式继续显示钱包卡和全 group 倍率列表。
- 老 group 订阅继续显示日/周/月用量进度、到期时间、续费按钮。
- 客户侧错误的 Opus 4.8 套餐权益横幅已删除。

业务边界：

- 只是弹窗复用和文案整理。
- 不改变订阅状态判断。
- 不改变月卡额度、消耗、覆盖分组或钱包 fallback。

### 4. 统一续费/充值弹窗 `RenewLiandongModal`

用户可见变化：

- 新增统一弹窗组件。
- 弹窗内包含：
  - 月卡 4 档。
  - 通用余额 3 档。
  - 自定义额度联系管理员微信。
  - 兑换码 30 天有效期提醒。
- 如果从某个订阅续费进入，会按当前订阅月配额高亮“同档推荐”。

业务边界：

- 只是前端弹窗和链动小铺跳转入口。
- 不改变支付回调、到账、钱包账本或套餐发放逻辑。

### 5. 兑换页 `/redeem`

用户可见变化：

- 兑换失败错误展示改用 `extractI18nErrorMessage`，能更好显示后端 i18n 错误。

业务边界：

- 不改变兑换码兑换规则。
- 不改变兑换码有效期计算。
- 不改变余额、并发或订阅发放逻辑。

### 6. 套餐卡 `SubscriptionPlanCard`

用户可见变化：

- 非 credits 计划且 supported model scopes 包含 `claude` 时，套餐卡增加高阶模型 badge：
  - `Opus 4.8`
  - `受额度保护`

业务边界：

- 只是展示 badge。
- 不代表自动启用官方 pricing。
- 不改变模型价格、倍率、分组覆盖、月卡覆盖或账本。

### 7. Dashboard 模型入口

用户可见变化：

- Dashboard 快捷模型选择中增加 `Claude Opus 4.8`。
- 模型 ID 使用 HFC 常量：`claude-opus-4-8`。
- 仍保留 `GPT-5.5` 作为默认模型。

业务边界：

- 只是快捷入口和候选展示。
- 请求后端仍按 HFC 模型映射、账号池和计费规则处理。
- 不套官方 platform quota 或 official pricing。

### 8. Opus 4.8 常量

新增 `frontend/src/constants/opus48.ts`，统一前端显示名和提示文案：

- `HFC_OPUS48_MODEL_ID = claude-opus-4-8`
- `HFC_OPUS48_DISPLAY_NAME = Claude Opus 4.8`
- `HFC_OPUS48_BADGE_LABEL = Opus 4.8`

注意：客户购买/订阅/续费页的 Opus 4.8 横幅已经删除；目前保留的是后台/套餐卡 badge/Dashboard 快捷入口这类展示。

### 9. 钱包订阅显示工具

`frontend/src/utils/subscriptionWallet.ts` 增强：

- `hasWalletBalance`
- `walletRemainingUsd`
- `walletInitialUsd`
- `walletUsedUsd`
- `walletUsedPercent`
- `formatWalletBalance`
- `formatWalletUsage`

业务边界：

- 只用于前端显示钱包余额、初始额度、使用进度。
- 不作为真实扣费依据。

## Admin-Facing Page Changes Related To This Review

虽然你这次问的是个人主页侧边栏和用户侧页面，但本次候选也有一些后台页面变化：

- 后台用户余额历史弹窗新增“充值余额”和“订阅钱包”拆分展示。
- 后台套餐编辑页保留 Opus 4.8 提示，提醒真实价格、倍率、覆盖分组仍以 HFC 后台配置为准。
- 后台运营页、用量页、渠道监控、兑换码批量编辑、风险控制等属于前面 safe-fusion 已拍板的管理增强，不新增客户侧菜单。

## What Did Not Change

本次候选没有做这些前端变化：

- 没有新增个人侧边栏入口。
- 没有改变个人侧边栏菜单排序。
- 没有把 OAuth/GitHub/Google/DingTalk/OIDC 登录入口开放到客户侧权益。
- 没有新增邮件通知、订阅提醒、Airwallex、多币种入口。
- 没有开放 embeddings 页面或入口。
- 没有把官方 IP ACL 作为客户侧黑名单入口。
- 没有把官方 platform quota 暴露给客户。
- 没有在客户购买/订阅/续费页保留 Opus 4.8 月卡误导横幅。

## Verification Notes

已确认：

- `frontend/src/components/layout/AppSidebar.vue` 相对当前候选交接基线无 diff。
- `frontend/src/router/index.ts` 相对当前候选交接基线无 diff。
- 客户侧错误 Opus 4.8 横幅在以下文件已清除：
  - `frontend/src/components/user/RenewLiandongModal.vue`
  - `frontend/src/views/user/PaymentView.vue`
  - `frontend/src/views/user/SubscriptionsView.vue`
- `pnpm --dir frontend typecheck` 已通过。
- `pnpm --dir frontend lint:check` 已通过。

仍需上线前补跑：

- 前端 build / Vitest：当前 Mac 被 Rollup native optional dependency/code-signature 问题阻塞。
- 生产上线前必须重新跑 HFC safe cutover、月卡 smoke、钱包 marker、健康检查、磁盘/Docker cache 检查。
