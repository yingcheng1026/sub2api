import { beforeEach, describe, expect, it, vi } from 'vitest'

const post = vi.hoisted(() => vi.fn())

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import {
  assign,
  bulkAssign,
  clearPendingBulkSubscriptionAssignment,
  clearPendingSubscriptionAssignment,
  getOrCreatePendingBulkSubscriptionAssignment,
  createSubscriptionAssignmentIdempotencyKey,
  getOrCreatePendingSubscriptionAssignment,
  isPendingSubscriptionAssignmentStale,
  readPendingBulkSubscriptionAssignments,
  readPendingSubscriptionAssignment,
  shouldRetainPendingSubscriptionAssignment
} from '@/api/admin/subscriptions'

const payload = { user_id: 9, wallet_initial_usd: 100 }
const subscription = {
  id: 42,
  user_id: 9,
  group_id: null,
  status: 'active',
  daily_usage_usd: 0,
  weekly_usage_usd: 0,
  monthly_usage_usd: 0,
  daily_window_start: null,
  weekly_window_start: null,
  monthly_window_start: null,
  wallet_balance_usd: 100,
  wallet_initial_usd: 100,
  created_at: '2026-07-12T00:00:00Z',
  updated_at: '2026-07-12T00:00:00Z',
  expires_at: '2099-12-31T23:59:59Z',
}

const bulkPayload = {
  user_ids: [9, 10],
  group_id: 7,
  validity_days: 30,
  notes: 'monthly grant'
}

const bulkResult = {
  success_count: 2,
  created_count: 1,
  reused_count: 1,
  failed_count: 0,
  subscriptions: [subscription],
  errors: [],
  statuses: { '9': 'created', '10': 'reused' }
}

function createStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
    removeItem: (key: string) => { values.delete(key) }
  }
}

describe('admin subscription assign idempotency', () => {
  let actorId = 100

  beforeEach(() => {
    actorId += 1
    post.mockReset().mockResolvedValue({ data: subscription })
  })

  it('sends the required Idempotency-Key header', async () => {
    await assign(payload, {
      idempotencyKey: 'admin-subscription-assign-test-1',
      networkRetries: 0
    })

    expect(post).toHaveBeenCalledWith(
      '/admin/subscriptions/assign',
      payload,
      { headers: { 'Idempotency-Key': 'admin-subscription-assign-test-1' } }
    )
  })

  it('reuses the same key for an ambiguous network retry', async () => {
    post
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockResolvedValueOnce({ data: subscription })

    await assign(payload, {
      idempotencyKey: 'admin-subscription-assign-retry-1',
      networkRetries: 1
    })

    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[0][2]).toEqual({
      headers: { 'Idempotency-Key': 'admin-subscription-assign-retry-1' }
    })
    expect(post.mock.calls[1][2]).toEqual(post.mock.calls[0][2])
  })

  it('recognizes and retries a raw Axios ERR_NETWORK response loss', async () => {
    post
      .mockRejectedValueOnce({ isAxiosError: true, code: 'ERR_NETWORK', request: {} })
      .mockResolvedValueOnce({ data: subscription })

    await assign(payload, {
      idempotencyKey: 'admin-subscription-assign-axios-network-1'
    })

    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[1][2]).toEqual(post.mock.calls[0][2])
  })

  it('creates a fresh key for each new logical submission', () => {
    const first = createSubscriptionAssignmentIdempotencyKey()
    const second = createSubscriptionAssignmentIdempotencyKey()

    expect(first).toMatch(/^admin-subscription-assign-/)
    expect(second).toMatch(/^admin-subscription-assign-/)
    expect(second).not.toBe(first)
  })

  it('fails locally instead of sending a request without a key', async () => {
    await expect(assign(payload, { idempotencyKey: '   ' })).rejects.toThrow(
      'Idempotency-Key is required'
    )

    expect(post).not.toHaveBeenCalled()
  })

  it('does not retry a definitive backend response', async () => {
    post.mockRejectedValueOnce({ status: 400, message: 'Invalid request' })

    await expect(
      assign(payload, { idempotencyKey: 'admin-subscription-assign-definitive-1' })
    ).rejects.toEqual({ status: 400, message: 'Invalid request' })

    expect(post).toHaveBeenCalledTimes(1)
  })

  it('restores the same pending key for the same payload across clicks and refreshes', () => {
    const storage = createStorage()
    const first = getOrCreatePendingSubscriptionAssignment(payload, actorId, storage)
    const restored = readPendingSubscriptionAssignment(actorId, storage)
    const secondClick = getOrCreatePendingSubscriptionAssignment({ ...payload }, actorId, storage)

    expect(restored).toEqual(first)
    expect(secondClick.idempotencyKey).toBe(first.idempotencyKey)
    expect(secondClick.fingerprint).toBe(first.fingerprint)
  })

  it('creates a new pending key only when the assignment payload changes', () => {
    const storage = createStorage()
    const first = getOrCreatePendingSubscriptionAssignment(payload, actorId, storage)
    const changed = getOrCreatePendingSubscriptionAssignment(
      { ...payload, wallet_initial_usd: 120 },
      actorId,
      storage
    )

    expect(changed.fingerprint).not.toBe(first.fingerprint)
    expect(changed.idempotencyKey).not.toBe(first.idempotencyKey)
  })

  it('keeps both unresolved payloads so A to B to A still reuses A key', () => {
    const storage = createStorage()
    const firstA = getOrCreatePendingSubscriptionAssignment(payload, actorId, storage)
    const attemptB = getOrCreatePendingSubscriptionAssignment(
      { ...payload, wallet_initial_usd: 120 },
      actorId,
      storage
    )
    const secondA = getOrCreatePendingSubscriptionAssignment(payload, actorId, storage)

    expect(attemptB.idempotencyKey).not.toBe(firstA.idempotencyKey)
    expect(secondA.idempotencyKey).toBe(firstA.idempotencyKey)
  })

  it('keeps the in-memory key when sessionStorage cannot persist writes', () => {
    const values = new Map<string, string>()
    const unavailableStorage = {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: () => { throw new Error('storage unavailable') },
      removeItem: (key: string) => { values.delete(key) }
    }
    const storageFailurePayload = { ...payload, wallet_initial_usd: 777 }
    const first = getOrCreatePendingSubscriptionAssignment(storageFailurePayload, actorId, unavailableStorage)
    const second = getOrCreatePendingSubscriptionAssignment(storageFailurePayload, actorId, unavailableStorage)

    expect(second.idempotencyKey).toBe(first.idempotencyKey)
  })

  it('marks old unknown outcomes for manual reconciliation instead of replacing their key', () => {
    const storage = createStorage()
    const attempt = getOrCreatePendingSubscriptionAssignment(payload, actorId, storage)
    const reconcileAt = attempt.createdAt + (30 * 60 * 1000)

    expect(isPendingSubscriptionAssignmentStale(attempt, reconcileAt - 1)).toBe(false)
    expect(isPendingSubscriptionAssignmentStale(attempt, reconcileAt)).toBe(true)
    expect(getOrCreatePendingSubscriptionAssignment(payload, actorId, storage).idempotencyKey).toBe(
      attempt.idempotencyKey
    )
  })

  it('clears only the matching completed or definitively rejected attempt', () => {
    const storage = createStorage()
    const first = getOrCreatePendingSubscriptionAssignment(payload, actorId, storage)
    const changed = getOrCreatePendingSubscriptionAssignment(
      { ...payload, wallet_initial_usd: 120 },
      actorId,
      storage
    )

    clearPendingSubscriptionAssignment(first, actorId, storage)
    expect(readPendingSubscriptionAssignment(actorId, storage)).toEqual(changed)

    clearPendingSubscriptionAssignment(changed, actorId, storage)
    expect(readPendingSubscriptionAssignment(actorId, storage)).toBeNull()
  })

  it('does not expose or reuse unresolved wallet assignments across admin actors', () => {
    const storage = createStorage()
    const adminA = getOrCreatePendingSubscriptionAssignment(payload, 901, storage)
    const adminB = getOrCreatePendingSubscriptionAssignment(payload, 902, storage)

    expect(adminB.idempotencyKey).not.toBe(adminA.idempotencyKey)
    expect(readPendingSubscriptionAssignment(901, storage)).toEqual(adminA)
    expect(readPendingSubscriptionAssignment(902, storage)).toEqual(adminB)
  })

  it('retains pending attempts for unknown outcomes but not definitive business errors', () => {
    expect(shouldRetainPendingSubscriptionAssignment({ status: 0 })).toBe(true)
    expect(shouldRetainPendingSubscriptionAssignment({ status: 502 })).toBe(true)
    expect(shouldRetainPendingSubscriptionAssignment({
      status: 409,
      reason: 'IDEMPOTENCY_IN_PROGRESS'
    })).toBe(true)
    expect(shouldRetainPendingSubscriptionAssignment({
      status: 400,
      reason: 'INVALID_INPUT'
    })).toBe(false)
  })
})

describe('admin subscription bulk assign idempotency', () => {
  beforeEach(() => {
    post.mockReset().mockResolvedValue({ data: bulkResult })
  })

  it('sends the required key and returns the backend bulk result object', async () => {
    const result = await bulkAssign(bulkPayload, {
      idempotencyKey: 'admin-subscription-bulk-assign-test-1',
      networkRetries: 0
    })

    expect(post).toHaveBeenCalledWith(
      '/admin/subscriptions/bulk-assign',
      bulkPayload,
      { headers: { 'Idempotency-Key': 'admin-subscription-bulk-assign-test-1' } }
    )
    expect(result).toEqual(bulkResult)
    expect(result.success_count).toBe(2)
    expect(result.statuses['10']).toBe('reused')
  })

  it('reuses the same bulk key for an ambiguous network retry', async () => {
    post
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockResolvedValueOnce({ data: bulkResult })

    await bulkAssign(bulkPayload, {
      idempotencyKey: 'admin-subscription-bulk-assign-retry-1'
    })

    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[1][2]).toEqual(post.mock.calls[0][2])
  })

  it('persists an A-B-A fingerprint map for the same admin across refreshes', () => {
    const storage = createStorage()
    const firstA = getOrCreatePendingBulkSubscriptionAssignment(bulkPayload, 51, storage)
    const attemptB = getOrCreatePendingBulkSubscriptionAssignment(
      { ...bulkPayload, user_ids: [11, 12] },
      51,
      storage
    )
    const secondA = getOrCreatePendingBulkSubscriptionAssignment(
      { ...bulkPayload, user_ids: [...bulkPayload.user_ids] },
      51,
      storage
    )

    expect(attemptB.idempotencyKey).not.toBe(firstA.idempotencyKey)
    expect(secondA.idempotencyKey).toBe(firstA.idempotencyKey)
    expect(readPendingBulkSubscriptionAssignments(51, storage)).toHaveLength(2)

    clearPendingBulkSubscriptionAssignment(firstA, 51, storage)
    expect(readPendingBulkSubscriptionAssignments(51, storage)).toEqual([attemptB])
  })

  it('namespaces persisted bulk attempts by admin actor', () => {
    const storage = createStorage()
    const adminA = getOrCreatePendingBulkSubscriptionAssignment(bulkPayload, 71, storage)
    const adminB = getOrCreatePendingBulkSubscriptionAssignment(bulkPayload, 72, storage)

    expect(adminB.idempotencyKey).not.toBe(adminA.idempotencyKey)
    expect(readPendingBulkSubscriptionAssignments(71, storage)).toEqual([adminA])
    expect(readPendingBulkSubscriptionAssignments(72, storage)).toEqual([adminB])
  })

  it('fails locally instead of sending a bulk request without a key', async () => {
    await expect(bulkAssign(bulkPayload, { idempotencyKey: ' ' })).rejects.toThrow(
      'Idempotency-Key is required'
    )
    expect(post).not.toHaveBeenCalled()
  })
})
