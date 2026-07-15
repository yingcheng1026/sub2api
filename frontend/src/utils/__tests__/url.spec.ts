import { describe, expect, it } from 'vitest'
import { sanitizeUrl } from '../url'

describe('sanitizeUrl', () => {
  it('normalizes an absolute HTTPS URL', () => {
    expect(sanitizeUrl(' https://example.com/path?q=1 ')).toBe('https://example.com/path?q=1')
  })

  it('rejects executable and protocol-relative destinations', () => {
    expect(sanitizeUrl('javascript:alert(1)')).toBe('')
    expect(sanitizeUrl('//evil.example/path')).toBe('')
  })

  it('rejects embedded credentials', () => {
    expect(sanitizeUrl('https://trusted.example@evil.example/path')).toBe('')
  })

  it('can require HTTPS for security-sensitive navigation', () => {
    expect(sanitizeUrl('http://example.com/path', { httpsOnly: true })).toBe('')
    expect(sanitizeUrl('https://example.com/path', { httpsOnly: true })).toBe('https://example.com/path')
  })
})
