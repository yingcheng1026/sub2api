/**
 * 链动小铺 SKU 常量 — 与 ai-relay-infra/scripts/endpoints.json 同步,改完两边都要更新
 *
 * Why hardcode 而不走 fetch endpoints.json:
 * - admin.handsfreeclub.com 跨子域 fetch 要 CORS,nginx 当前没配
 * - 8 个 URL 一年改不了几次,build 一次成本可控
 */

export interface LiandongMonthlyTier {
  id: 'trial' | 'lite' | 'standard' | 'pro' | 'flagship'
  /** 月度配额 USD,与 group.monthly_limit_usd 匹配 */
  quotaUsd: number
  /** 每日 cap USD;null = 不限 */
  dailyCapUsd: number | null
  /** 链动小铺 SKU URL */
  url: string
  /** 商品名,用于按钮文案 */
  name: string
  /** 售价 CNY */
  priceCny: number
  /** 官网划线原价 CNY */
  originalPriceCny: number
  /** 官网套餐一句话描述 */
  tagline: string
  /** 用户购买页展示要点 */
  features: readonly string[]
  /** 首页推荐标记 */
  recommended?: boolean
}

export interface LiandongCreditsTier {
  /** 通用余额 USD 面值 (1¥=$1) */
  creditsUsd: number
  url: string
  name: string
  priceCny: number
}

/** 月卡 5 档 — 与 endpoints.json `pricing_links.tiers` 对齐 */
export const LIANDONG_MONTHLY_TIERS: readonly LiandongMonthlyTier[] = [
  {
    id: 'trial',
    quotaUsd: 100,
    dailyCapUsd: null,
    priceCny: 29.9,
    originalPriceCny: 49.9,
    name: '体验版',
    url: 'https://pay.ldxp.cn/item/z80wd7',
    tagline: '注册送 $15,先试再买',
    features: ['先把链路验证一遍', '低频使用 / 评估迁移', '先用 $100 月额度验证链路']
  },
  {
    id: 'lite',
    quotaUsd: 400,
    dailyCapUsd: 50,
    priceCny: 99,
    originalPriceCny: 199,
    name: '轻量正式版',
    url: 'https://pay.ldxp.cn/item/neu4dr',
    tagline: '轻量 Claude Code / GPT 日常使用',
    features: ['轻量 Claude Code / GPT 日常使用', '$400 月度钱包额度', '每日 cap $50']
  },
  {
    id: 'standard',
    quotaUsd: 1500,
    dailyCapUsd: 50,
    priceCny: 299,
    originalPriceCny: 499,
    name: '标准版',
    url: 'https://pay.ldxp.cn/item/6zkn8r',
    tagline: '日常 Claude Code 主力',
    features: ['日常 Claude Code 主力开发', '多用户的最佳起点', '平衡成本与可用性'],
    recommended: true
  },
  {
    id: 'pro',
    quotaUsd: 3000,
    dailyCapUsd: 100,
    priceCny: 450,
    originalPriceCny: 999,
    name: '进阶版',
    url: 'https://pay.ldxp.cn/item/zxarhv',
    tagline: '高强度持续工作 + 长任务',
    features: ['高强度持续工作', '长任务 + 高并行场景', '把 Claude Code 当主工作台']
  },
  {
    id: 'flagship',
    quotaUsd: 4500,
    dailyCapUsd: 300,
    priceCny: 899,
    originalPriceCny: 1499,
    name: '旗舰版',
    url: 'https://pay.ldxp.cn/item/bdu9vx',
    tagline: '团队 / 重型自动化 / 单价最低',
    features: ['团队 / 重型自动化', '无界 agent 流水线场景', '把单价压到极致']
  }
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
