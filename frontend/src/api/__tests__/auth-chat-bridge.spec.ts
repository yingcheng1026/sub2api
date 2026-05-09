import { beforeEach, describe, expect, it, vi } from 'vitest'

const post = vi.fn()

vi.mock('@/api/client', () => ({
  apiClient: {
    post
  }
}))

describe('chat bridge auth api', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({
      data: {
        chat_url: 'https://chat.handsfreeclub.com/agent/inbox?hfc_chat_code=code-123',
        code: 'code-123',
        expires_in: 60
      }
    })
  })

  it('creates a chat bridge code for the inbox path', async () => {
    const { createChatBridgeCode } = await import('@/api/auth')

    const result = await createChatBridgeCode({ redirect_path: '/agent/inbox' })

    expect(post).toHaveBeenCalledWith('/chat/bridge/code', { redirect_path: '/agent/inbox' })
    expect(result.code).toBe('code-123')
    expect(result.chat_url).toContain('hfc_chat_code=code-123')
  })
})
