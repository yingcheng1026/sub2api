import { describe, expect, it } from 'vitest'
import { resolveSubscriptionConsumedUSD } from '../subscriptionConsumed'

describe('resolveSubscriptionConsumedUSD', () => {
  it('returns the backend consumed_usd verbatim when present (covers standard groups)', () => {
    expect(
      resolveSubscriptionConsumedUSD({
        monthly_usage_usd: 0,
        consumed_usd: 7.5
      })
    ).toBe(7.5)
  })

  it('falls back to monthly_usage_usd when the API response predates consumed_usd', () => {
    expect(
      resolveSubscriptionConsumedUSD({
        monthly_usage_usd: 42
      })
    ).toBe(42)
  })

  it('returns 0 when neither field is set', () => {
    expect(resolveSubscriptionConsumedUSD({})).toBe(0)
  })

  it('treats explicit zero from backend as authoritative (no silent fallback)', () => {
    expect(
      resolveSubscriptionConsumedUSD({
        monthly_usage_usd: 99,
        consumed_usd: 0
      })
    ).toBe(0)
  })
})
