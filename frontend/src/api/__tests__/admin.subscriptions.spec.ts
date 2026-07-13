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
  assign,
  createSubscriptionAssignmentIdempotencyKey,
} from '@/api/admin/subscriptions'

describe('admin subscription assignment API', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('sends the same logical assignment key in the Idempotency-Key header', async () => {
    const request = { user_id: 173, wallet_initial_usd: 50 }
    post.mockResolvedValue({ data: { id: 88 } })

    await assign(request, 'admin-subscription-assign-test-1')

    expect(post).toHaveBeenCalledWith('/admin/subscriptions/assign', request, {
      headers: { 'Idempotency-Key': 'admin-subscription-assign-test-1' },
    })
  })

  it('creates non-empty assignment keys with a dedicated prefix', () => {
    const first = createSubscriptionAssignmentIdempotencyKey()
    const second = createSubscriptionAssignmentIdempotencyKey()

    expect(first).toMatch(/^admin-subscription-assign-/)
    expect(second).toMatch(/^admin-subscription-assign-/)
    expect(first).not.toBe(second)
  })

  it('refuses to send a financial assignment without a key', async () => {
    await expect(assign({ user_id: 173, wallet_initial_usd: 50 }, '   ')).rejects.toThrow(
      'Idempotency-Key is required'
    )
    expect(post).not.toHaveBeenCalled()
  })
})
