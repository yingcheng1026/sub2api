import { beforeEach, describe, expect, it, vi } from 'vitest'

const post = vi.hoisted(() => vi.fn())

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { updateBalance } from '@/api/admin/users'
import { generate } from '@/api/admin/redeem'
import { extend } from '@/api/admin/subscriptions'

describe('admin financial write idempotency', () => {
  beforeEach(() => {
    post.mockReset().mockResolvedValue({ data: { id: 1 } })
  })

  it('always sends generated keys for balance, redeem generation, and subscription extension', async () => {
    await updateBalance(7, 25, 'add', 'manual topup')
    await generate(2, 'balance', 10)
    await extend(9, { days: 30 })

    expect(post.mock.calls[0][2].headers['Idempotency-Key']).toMatch(/^admin-users-balance-/)
    expect(post.mock.calls[1][2].headers['Idempotency-Key']).toMatch(/^admin-redeem-generate-/)
    expect(post.mock.calls[2][2].headers['Idempotency-Key']).toMatch(/^admin-subscription-extend-/)
  })

  it('reuses one key for the bounded retry of an ambiguous response loss', async () => {
    post
      .mockRejectedValueOnce({ isAxiosError: true, code: 'ERR_NETWORK' })
      .mockResolvedValueOnce({ data: { id: 7 } })

    await updateBalance(7, 25, 'add', 'manual topup')

    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[1][2]).toEqual(post.mock.calls[0][2])
  })

  it('accepts a caller-owned key so a logical retry can survive the first API call', async () => {
    await extend(
      9,
      { days: 30 },
      { idempotencyKey: 'admin-subscription-extend-stable-1', networkRetries: 0 }
    )

    expect(post.mock.calls[0][2]).toEqual({
      headers: { 'Idempotency-Key': 'admin-subscription-extend-stable-1' }
    })
  })

  it('sends the credits plan id when generating wallet redeem codes', async () => {
	await generate(3, 'wallet', 100, undefined, undefined, 42)

	expect(post.mock.calls[0][0]).toBe('/admin/redeem-codes/generate')
	expect(post.mock.calls[0][1]).toEqual({
	  count: 3,
	  type: 'wallet',
	  value: 100,
	  plan_id: 42
	})
  })
})
