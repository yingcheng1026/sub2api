import { describe, expect, it } from 'vitest'
import {
  applyWriteOnlyMonitorCustomization,
  applyWriteOnlyTemplateCustomization,
} from '../channelMonitorWriteOnly'

describe('applyWriteOnlyMonitorCustomization', () => {
  it('omits write-only fields on an ordinary edit', () => {
    const result = applyWriteOnlyMonitorCustomization(
      { name: 'unchanged-sensitive-config' },
      72,
      {
        templateId: 72,
        replace: false,
        extraHeaders: { 'X-Private': 'must-not-send' },
        bodyOverrideMode: 'replace',
        bodyOverride: { token: 'must-not-send' },
      },
    )

    expect(result).toEqual({ name: 'unchanged-sensitive-config' })
  })

  it('applies a selected template by id without returning template contents to the browser', () => {
    const result = applyWriteOnlyMonitorCustomization(
      { provider: 'anthropic' },
      null,
      {
        templateId: 72,
        replace: true,
        extraHeaders: { 'X-Private': 'must-not-send' },
        bodyOverrideMode: 'replace',
        bodyOverride: { token: 'must-not-send' },
      },
    )

    expect(result).toEqual({
      provider: 'anthropic',
      replace_request_customization: true,
      template_id: 72,
    })
  })

  it('sends an explicit atomic replacement when clearing a template', () => {
    const result = applyWriteOnlyMonitorCustomization(
      { provider: 'anthropic' },
      72,
      {
        templateId: null,
        replace: true,
        extraHeaders: {},
        bodyOverrideMode: 'off',
        bodyOverride: null,
      },
    )

    expect(result).toEqual({
      provider: 'anthropic',
      replace_request_customization: true,
      clear_template: true,
      extra_headers: {},
      body_override_mode: 'off',
      body_override: null,
    })
  })
})

describe('applyWriteOnlyTemplateCustomization', () => {
  it('preserves the stored payload when only template metadata changes', () => {
    const result = applyWriteOnlyTemplateCustomization(
      {
        name: 'renamed',
        extra_headers: { 'X-Private': 'must-not-send' },
        body_override_mode: 'replace',
        body_override: { token: 'must-not-send' },
      },
      {
        replace: false,
        extraHeaders: {},
        bodyOverrideMode: 'off',
        bodyOverride: null,
      },
    )

    expect(result).toEqual({ name: 'renamed' })
  })

  it('sends all replacement fields as one explicit operation', () => {
    const result = applyWriteOnlyTemplateCustomization(
      { name: 'replaced' },
      {
        replace: true,
        extraHeaders: { 'X-Trace': 'new-value' },
        bodyOverrideMode: 'merge',
        bodyOverride: { system: 'new-body' },
      },
    )

    expect(result).toEqual({
      name: 'replaced',
      replace_request_customization: true,
      extra_headers: { 'X-Trace': 'new-value' },
      body_override_mode: 'merge',
      body_override: { system: 'new-body' },
    })
  })
})
