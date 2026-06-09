import { describe, expect, it, vi, beforeEach } from 'vitest'

const postMock = vi.fn()

vi.mock('../client', () => ({
  apiClient: {
    post: postMock,
  },
}))

import { ACCOUNT_BULK_UPDATE_TIMEOUT_MS, bulkUpdate } from '../admin/accounts'

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
})
