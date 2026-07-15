import { describe, expect, it } from 'vitest'
import { LIANDONG_CREDITS_TIERS } from '../liandongSku'

describe('liandongSku', () => {
  it('only exposes the three credit top-up SKUs', () => {
    expect(LIANDONG_CREDITS_TIERS).toEqual([
      { creditsUsd: 30, priceCny: 30, name: '$30 通用余额', url: 'https://pay.ldxp.cn/item/bbs9ki' },
      { creditsUsd: 100, priceCny: 100, name: '$100 通用余额', url: 'https://pay.ldxp.cn/item/b4nrv0' },
      { creditsUsd: 500, priceCny: 500, name: '$500 通用余额', url: 'https://pay.ldxp.cn/item/o5isg4' }
    ])
  })
})
