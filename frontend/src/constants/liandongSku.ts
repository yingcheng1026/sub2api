/**
 * 链动小铺 SKU 常量 — 与 ai-relay-infra/scripts/endpoints.json 同步,改完两边都要更新
 *
 * Why hardcode 而不走 fetch endpoints.json:
 * - admin.handsfreeclub.com 跨子域 fetch 要 CORS,nginx 当前没配
 * - 3 个 URL 很少变更，build 一次成本可控
 */

export interface LiandongCreditsTier {
  /** 通用余额 USD 面值 (1¥=$1) */
  creditsUsd: number
  url: string
  name: string
  priceCny: number
}

/** 通用余额 3 档 — 与 endpoints.json `credits_links.tiers` 对齐 */
export const LIANDONG_CREDITS_TIERS: readonly LiandongCreditsTier[] = [
  { creditsUsd: 30, priceCny: 30, name: '$30 通用余额', url: 'https://pay.ldxp.cn/item/bbs9ki' },
  { creditsUsd: 100, priceCny: 100, name: '$100 通用余额', url: 'https://pay.ldxp.cn/item/b4nrv0' },
  { creditsUsd: 500, priceCny: 500, name: '$500 通用余额', url: 'https://pay.ldxp.cn/item/o5isg4' }
] as const

/** 自定义额度场景的引导联系微信 */
export const LIANDONG_CUSTOM_WECHAT = 'climb102626'
