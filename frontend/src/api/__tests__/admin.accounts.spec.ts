import { describe, expect, it, vi, beforeEach } from 'vitest'

const postMock = vi.fn()

vi.mock('../client', () => ({
  apiClient: {
    post: postMock,
  },
}))

import { ACCOUNT_BULK_UPDATE_CHUNK_SIZE, ACCOUNT_BULK_UPDATE_TIMEOUT_MS, bulkUpdate } from '../admin/accounts'

describe('admin accounts API', () => {
  beforeEach(() => {
    postMock.mockReset()
  })

  it('uses an extended timeout for account bulk updates', async () => {
    postMock.mockResolvedValue({
      data: {
        success: 2,
        failed: 0,
        results: [],
      },
    })

    await bulkUpdate([1, 2], { group_ids: [3, 7] })

    expect(postMock).toHaveBeenCalledWith(
      '/admin/accounts/bulk-update',
      { account_ids: [1, 2], group_ids: [3, 7] },
      { timeout: ACCOUNT_BULK_UPDATE_TIMEOUT_MS },
    )
  })

  it('turns bulk update timeouts into an actionable error', async () => {
    postMock.mockRejectedValue({ status: 0, code: 'ECONNABORTED', message: 'timeout of 180000ms exceeded' })

    await expect(bulkUpdate([1], { group_ids: [3] })).rejects.toMatchObject({
      status: 0,
      message: expect.stringContaining('refresh the account list before retrying'),
    })
  })

  it('does not send a bulk update request for an empty selected account list', async () => {
    const result = await bulkUpdate([], { group_ids: [3] })

    expect(postMock).not.toHaveBeenCalled()
    expect(result).toEqual({
      success: 0,
      failed: 0,
      success_ids: [],
      failed_ids: [],
      results: [],
    })
  })

  it('splits selected account bulk updates into bounded chunks and reports progress', async () => {
    const accountIds = Array.from({ length: ACCOUNT_BULK_UPDATE_CHUNK_SIZE + 3 }, (_, index) => index + 1)
    const onProgress = vi.fn()
    postMock
      .mockResolvedValueOnce({
        data: {
          success: ACCOUNT_BULK_UPDATE_CHUNK_SIZE,
          failed: 0,
          success_ids: accountIds.slice(0, ACCOUNT_BULK_UPDATE_CHUNK_SIZE),
          failed_ids: [],
          results: accountIds.slice(0, ACCOUNT_BULK_UPDATE_CHUNK_SIZE).map((accountID) => ({
            account_id: accountID,
            success: true,
          })),
        },
      })
      .mockResolvedValueOnce({
        data: {
          success: 3,
          failed: 0,
          success_ids: accountIds.slice(ACCOUNT_BULK_UPDATE_CHUNK_SIZE),
          failed_ids: [],
          results: accountIds.slice(ACCOUNT_BULK_UPDATE_CHUNK_SIZE).map((accountID) => ({
            account_id: accountID,
            success: true,
          })),
        },
      })

    const result = await bulkUpdate(accountIds, { group_ids: [3, 7] }, { onProgress })

    expect(postMock).toHaveBeenCalledTimes(2)
    expect(postMock).toHaveBeenNthCalledWith(
      1,
      '/admin/accounts/bulk-update',
      { account_ids: accountIds.slice(0, ACCOUNT_BULK_UPDATE_CHUNK_SIZE), group_ids: [3, 7] },
      { timeout: ACCOUNT_BULK_UPDATE_TIMEOUT_MS },
    )
    expect(postMock).toHaveBeenNthCalledWith(
      2,
      '/admin/accounts/bulk-update',
      { account_ids: accountIds.slice(ACCOUNT_BULK_UPDATE_CHUNK_SIZE), group_ids: [3, 7] },
      { timeout: ACCOUNT_BULK_UPDATE_TIMEOUT_MS },
    )
    expect(result.success).toBe(ACCOUNT_BULK_UPDATE_CHUNK_SIZE + 3)
    expect(result.failed).toBe(0)
    expect(result.success_ids).toEqual(accountIds)
    expect(result.results).toHaveLength(ACCOUNT_BULK_UPDATE_CHUNK_SIZE + 3)
    expect(onProgress).toHaveBeenNthCalledWith(1, {
      processed: ACCOUNT_BULK_UPDATE_CHUNK_SIZE,
      total: ACCOUNT_BULK_UPDATE_CHUNK_SIZE + 3,
      chunkIndex: 1,
      chunkCount: 2,
    })
    expect(onProgress).toHaveBeenNthCalledWith(2, {
      processed: ACCOUNT_BULK_UPDATE_CHUNK_SIZE + 3,
      total: ACCOUNT_BULK_UPDATE_CHUNK_SIZE + 3,
      chunkIndex: 2,
      chunkCount: 2,
    })
  })
})
