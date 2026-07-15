import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

describe('BackupView restore credential handling', () => {
  it('uses an in-app password field and never window.prompt', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/views/admin/BackupView.vue'), 'utf8')

    expect(source).not.toContain('window.prompt')
    expect(source).toContain('data-test="backup-restore-password"')
    expect(source).toContain('autocomplete="current-password"')
  })
})
