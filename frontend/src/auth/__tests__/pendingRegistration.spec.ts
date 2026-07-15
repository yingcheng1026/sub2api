import { beforeEach, describe, expect, it } from 'vitest'
import {
  clearPendingRegistration,
  consumePendingRegistration,
  setPendingRegistration,
} from '../pendingRegistration'

describe('pendingRegistration', () => {
  beforeEach(() => {
    clearPendingRegistration()
    window.sessionStorage.clear()
    window.localStorage.clear()
  })

  it('keeps registration credentials out of browser storage', () => {
    setPendingRegistration({ email: 'person@example.com', password: 'plain-secret' })

    expect(JSON.stringify(window.sessionStorage)).not.toContain('plain-secret')
    expect(JSON.stringify(window.localStorage)).not.toContain('plain-secret')
    expect(window.sessionStorage.getItem('register_data')).toBeNull()
  })

  it('returns the credentials once and then expires them', () => {
    setPendingRegistration({ email: 'person@example.com', password: 'plain-secret' })

    expect(consumePendingRegistration()).toEqual({
      email: 'person@example.com',
      password: 'plain-secret',
    })
    expect(consumePendingRegistration()).toBeNull()
  })
})
