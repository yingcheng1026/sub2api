import { describe, expect, it } from 'vitest'
import { sanitizeHtml } from '@/utils/sanitize'

describe('sanitizeHtml', () => {
  it('removes executable markup while preserving safe home-page HTML', () => {
    const sanitized = sanitizeHtml(`
      <section><h1>Welcome</h1><img src="/logo.png" onerror="window.stolen=localStorage.auth_token"></section>
      <script>window.stolen = localStorage.refresh_token</script>
      <a href="javascript:window.stolen=1">unsafe</a>
    `)

    expect(sanitized).toContain('<section>')
    expect(sanitized).toContain('<h1>Welcome</h1>')
    expect(sanitized).not.toContain('onerror')
    expect(sanitized).not.toContain('<script')
    expect(sanitized).not.toContain('javascript:')
  })
})
