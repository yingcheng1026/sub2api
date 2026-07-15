import { beforeEach, describe, expect, it, vi } from 'vitest'

const post = vi.hoisted(() => vi.fn())
const put = vi.hoisted(() => vi.fn())

vi.mock('@/api/client', () => ({ apiClient: { post, put } }))

import { create, update } from '@/api/keys'

describe('API key step-up requests', () => {
  beforeEach(() => {
    post.mockReset().mockResolvedValue({ data: { id: 1, key: 'sk-generated' } })
    put.mockReset().mockResolvedValue({ data: { id: 1, key: 'sk-masked' } })
  })

  it('sends fresh verification only in the create request body', async () => {
    await create('secure key', 7, undefined, [], [], 0, undefined, undefined, 'fresh-proof')

    expect(post).toHaveBeenCalledWith('/keys', {
      name: 'secure key',
      group_id: 7,
      verification: 'fresh-proof'
    })
  })

  it('sends fresh verification with sensitive key updates', async () => {
    await update(1, { ip_whitelist: [], verification: 'fresh-proof' })

    expect(put).toHaveBeenCalledWith('/keys/1', {
      ip_whitelist: [],
      verification: 'fresh-proof'
    })
  })
})
