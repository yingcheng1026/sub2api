import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn()
}))

vi.mock('../client', () => ({
  apiClient: {
    get
  }
}))

import * as systemModule from '@/api/admin/system'

const sourcePath = resolve(dirname(fileURLToPath(import.meta.url)), '../admin/system.ts')
const source = readFileSync(sourcePath, 'utf8')

describe('admin system API (immutable deployment surface)', () => {
  beforeEach(() => {
    get.mockReset()
  })

  it('exports read-only version helpers', async () => {
    expect(typeof systemModule.getVersion).toBe('function')
    expect(typeof systemModule.checkUpdates).toBe('function')
  })

  it('does not export mutable update, rollback, or restart helpers', () => {
    expect(systemModule).not.toHaveProperty('performUpdate')
    expect(systemModule).not.toHaveProperty('rollback')
    expect(systemModule).not.toHaveProperty('restartService')
  })

  it('does not reference mutable admin system write endpoints in source', () => {
    expect(source).not.toContain("'/admin/system/update'")
    expect(source).not.toContain("'/admin/system/rollback'")
    expect(source).not.toContain("'/admin/system/restart'")
  })

  it('does not reference the mutable timeout constant or update result type', () => {
    expect(source).not.toContain('UPDATE_REQUEST_TIMEOUT_MS')
    expect(source).not.toContain('UpdateResult')
    expect(source).not.toContain('RollbackVersionInfo')
  })

  it('getVersion fetches current version via GET', async () => {
    get.mockResolvedValue({ data: { version: '0.1.132' } })

    const result = await systemModule.getVersion()

    expect(get).toHaveBeenCalledWith('/admin/system/version')
    expect(result.version).toBe('0.1.132')
  })

  it('checkUpdates fetches version info via GET', async () => {
    get.mockResolvedValue({
      data: {
        current_version: '0.1.132',
        latest_version: '0.1.146',
        has_update: true,
        cached: false,
        build_type: 'release'
      }
    })

    const result = await systemModule.checkUpdates(true)

    expect(get).toHaveBeenCalledWith('/admin/system/check-updates', {
      params: { force: 'true' }
    })
    expect(result.has_update).toBe(true)
  })
})
