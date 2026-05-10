import { describe, expect, it } from 'vitest'
import { buildChatBridgeFallbackUrl, resolveChatReturn } from '../chatReturn'

describe('chat return redirect', () => {
  it('accepts chat.handsfreeclub.com and keeps its query', () => {
    expect(
      resolveChatReturn('https://chat.handsfreeclub.com/agent/inbox?hfc_model=gpt-5.4-mini')
    ).toEqual({
      redirectPath: '/?hfc_model=gpt-5.4-mini'
    })
  })

  it('uses root when the return target is the chat origin', () => {
    expect(resolveChatReturn('https://chat.handsfreeclub.com')).toEqual({
      redirectPath: '/'
    })
  })

  it('rejects non-chat and malformed return targets', () => {
    expect(resolveChatReturn('https://evil.example.com')).toBeNull()
    expect(resolveChatReturn('/dashboard')).toBeNull()
    expect(resolveChatReturn('not a url')).toBeNull()
  })

  it('removes stale bridge codes from accepted return targets', () => {
    expect(
      resolveChatReturn('https://chat.handsfreeclub.com/agent/inbox?hfc_chat_code=old&hfc_model=gpt-5.4')
    ).toEqual({
      redirectPath: '/?hfc_model=gpt-5.4'
    })
  })

  it('normalizes hash routes to the chat home because the server cannot read URL fragments', () => {
    expect(resolveChatReturn('https://chat.handsfreeclub.com/#/auth')).toEqual({
      redirectPath: '/'
    })
  })

  it('builds a fallback bridge URL when the backend response omits chat_url', () => {
    expect(buildChatBridgeFallbackUrl('code-123', '/?hfc_model=gpt-5.4')).toBe(
      'https://chat.handsfreeclub.com/?hfc_model=gpt-5.4&hfc_chat_code=code-123'
    )
  })
})
