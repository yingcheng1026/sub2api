import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post,
  },
}))

import {
  createRedeemGenerationIdempotencyKey,
  generate,
} from '@/api/admin/redeem'

describe('admin redeem generation API', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('sends the logical generation key in the Idempotency-Key header', async () => {
    post.mockResolvedValue({ data: [{ id: 371, code: 'HFC-CODE' }] })

    await generate(1, 'wallet', 30, undefined, undefined, 11, 'admin-redeem-generate-test-1')

    expect(post).toHaveBeenCalledWith(
      '/admin/redeem-codes/generate',
      { count: 1, type: 'wallet', value: 30, plan_id: 11 },
      { headers: { 'Idempotency-Key': 'admin-redeem-generate-test-1' } },
    )
  })

  it('creates non-empty generation keys with a dedicated prefix', () => {
    const first = createRedeemGenerationIdempotencyKey()
    const second = createRedeemGenerationIdempotencyKey()

    expect(first).toMatch(/^admin-redeem-generate-/)
    expect(second).toMatch(/^admin-redeem-generate-/)
    expect(first).not.toBe(second)
  })

  it('refuses to generate financial inventory without a key', async () => {
    await expect(generate(1, 'wallet', 30, undefined, undefined, 11, '   ')).rejects.toThrow(
      'Idempotency-Key is required',
    )
    expect(post).not.toHaveBeenCalled()
  })
})
