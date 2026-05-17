/**
 * 链动小铺 SKU 常量 — 与 ai-relay-infra/scripts/endpoints.json 同步,改完两边都要更新
 *
 * Why hardcode 而不走 fetch endpoints.json:
 * - admin.handsfreeclub.com 跨子域 fetch 要 CORS,nginx 当前没配
 * - 7 个 URL 一年改不了几次,build 一次成本可控
 */

export interface LiandongMonthlyTier {
  /** 月度配额 USD,与 group.monthly_limit_usd 匹配 */
  quotaUsd: number
  /** 链动小铺 SKU URL */
  url: string
  /** 商品名,用于按钮文案 */
  name: string
  /** 售价 CNY */
  priceCny: number
}

export interface LiandongCreditsTier {
  /** 通用余额 USD 面值 (1¥=$1) */
  creditsUsd: number
  url: string
  name: string
  priceCny: number
}

/** 月卡 4 档 — 与 endpoints.json `pricing_links.tiers` 对齐 */
export const LIANDONG_MONTHLY_TIERS: readonly LiandongMonthlyTier[] = [
  { quotaUsd: 100, priceCny: 29.9, name: '体验版', url: 'https://pay.ldxp.cn/item/z80wd7' },
  { quotaUsd: 1500, priceCny: 299, name: '标准版', url: 'https://pay.ldxp.cn/item/6zkn8r' },
  { quotaUsd: 3000, priceCny: 450, name: '进阶版', url: 'https://pay.ldxp.cn/item/zxarhv' },
  { quotaUsd: 15000, priceCny: 899, name: '旗舰版', url: 'https://pay.ldxp.cn/item/bdu9vx' }
] as const

/** 通用余额 3 档 — 与 endpoints.json `credits_links.tiers` 对齐 */
export const LIANDONG_CREDITS_TIERS: readonly LiandongCreditsTier[] = [
  { creditsUsd: 30, priceCny: 30, name: '$30 通用余额', url: 'https://pay.ldxp.cn/item/bbs9ki' },
  { creditsUsd: 100, priceCny: 100, name: '$100 通用余额', url: 'https://pay.ldxp.cn/item/b4nrv0' },
  { creditsUsd: 500, priceCny: 500, name: '$500 通用余额', url: 'https://pay.ldxp.cn/item/o5isg4' }
] as const

/** 自定义额度 / 续费疑难场景的引导联系微信 */
export const LIANDONG_CUSTOM_WECHAT = 'climb102626'

/** 按订阅当前 monthly_limit_usd 匹配同档月卡;无匹配返回 null */
export function matchMonthlyTier(
  monthlyLimitUsd: number | null | undefined
): LiandongMonthlyTier | null {
  if (monthlyLimitUsd == null) return null
  return LIANDONG_MONTHLY_TIERS.find((t) => t.quotaUsd === Number(monthlyLimitUsd)) ?? null
}
