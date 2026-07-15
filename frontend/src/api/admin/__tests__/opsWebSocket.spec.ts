import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

const post = vi.hoisted(() => vi.fn())

vi.mock('@/api/client', () => ({
  apiClient: { post },
}))

class MockWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 3
  static instances: MockWebSocket[] = []

  readonly url: string
  readonly protocols: string[]
  readyState = MockWebSocket.CONNECTING
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null

  constructor(url: string, protocols: string | string[]) {
    this.url = url
    this.protocols = Array.isArray(protocols) ? protocols : [protocols]
    MockWebSocket.instances.push(this)
  }

  close(): void {
    this.readyState = MockWebSocket.CLOSED
  }
}

import { subscribeQPS } from '../ops'

describe('admin Ops WebSocket authentication', () => {
  beforeEach(() => {
    post.mockReset().mockResolvedValue({
      data: {
        ticket: 'short-lived-ws-ticket',
        expires_at: '2026-07-14T04:00:20Z',
        expires_in: 20,
      },
    })
    MockWebSocket.instances = []
    vi.stubGlobal('WebSocket', MockWebSocket)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('obtains a scoped ticket before connecting and never sends the admin access JWT', async () => {
    const stop = subscribeQPS(() => {}, {
      maxReconnectAttempts: 0,
      staleTimeoutMs: 0,
      wsBaseUrl: 'admin.example.com',
    })
    await flushPromises()

    expect(post).toHaveBeenCalledWith('/admin/ops/ws/ticket', {})
    expect(MockWebSocket.instances).toHaveLength(1)
    expect(MockWebSocket.instances[0]?.url).toBe('ws://admin.example.com/api/v1/admin/ops/ws/qps')
    expect(MockWebSocket.instances[0]?.protocols).toEqual([
      'sub2api-admin',
      'ticket.short-lived-ws-ticket',
    ])
    expect(MockWebSocket.instances[0]?.protocols.join(',')).not.toContain('jwt.')

    stop()
  })
})
