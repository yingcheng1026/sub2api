import { describe, expect, it } from 'vitest'
import { LIANDONG_MONTHLY_TIERS, matchMonthlyTier } from '../liandongSku'

describe('liandongSku', () => {
  it('includes the paid-lite monthly tier used by renewal and dashboard top-up modals', () => {
    expect(LIANDONG_MONTHLY_TIERS.map((tier) => tier.quotaUsd)).toEqual([
      100,
      400,
      1500,
      3000,
      4500,
    ])

    expect(matchMonthlyTier(400)).toMatchObject({
      id: 'lite',
      quotaUsd: 400,
      dailyCapUsd: 50,
      priceCny: 99,
      name: '轻量正式版',
      url: 'https://pay.ldxp.cn/item/neu4dr',
    })
    expect(matchMonthlyTier(4500)).toMatchObject({
      id: 'flagship',
      dailyCapUsd: 300,
      url: 'https://pay.ldxp.cn/item/bdu9vx',
    })
  })
})
