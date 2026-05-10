// 后端的 standard 类型分组（如 paid-trial-bonus）从不写 monthly_usage_usd，
// 它们的累计消耗由后端从 usage_logs 聚合后填进 consumed_usd。
// 老接口、老缓存可能没有 consumed_usd 字段，所以保留 monthly_usage_usd 作为兜底。
// 注意：consumed_usd === 0 是后端权威值（"已消耗 $0"），不应被 monthly_usage_usd 覆盖。
export interface SubscriptionConsumedSource {
  consumed_usd?: number | null
  monthly_usage_usd?: number | null
}

export function resolveSubscriptionConsumedUSD(row: SubscriptionConsumedSource): number {
  if (typeof row.consumed_usd === 'number') {
    return row.consumed_usd
  }
  if (typeof row.monthly_usage_usd === 'number') {
    return row.monthly_usage_usd
  }
  return 0
}
