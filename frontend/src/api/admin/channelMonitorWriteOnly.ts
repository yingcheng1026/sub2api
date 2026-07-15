import type { BodyOverrideMode, UpdateParams } from './channelMonitor'
import type { UpdateParams as TemplateUpdateParams } from './channelMonitorTemplate'

export interface WriteOnlyMonitorCustomization {
  templateId: number | null
  replace: boolean
  extraHeaders: Record<string, string>
  bodyOverrideMode: BodyOverrideMode
  bodyOverride: Record<string, unknown> | null
}

/**
 * Adds the atomic write-only customization operation to an ordinary monitor
 * update. Unchanged sensitive fields are deliberately omitted, so a browser
 * can edit unrelated properties without receiving or erasing stored secrets.
 */
export function applyWriteOnlyMonitorCustomization(
  request: UpdateParams,
  originalTemplateId: number | null,
  customization: WriteOnlyMonitorCustomization,
): UpdateParams {
  const safeRequest = { ...request }
  delete safeRequest.template_id
  delete safeRequest.clear_template
  delete safeRequest.extra_headers
  delete safeRequest.body_override_mode
  delete safeRequest.body_override
  delete safeRequest.replace_request_customization
  const templateChanged = customization.templateId !== originalTemplateId
  if (!customization.replace && !templateChanged) return safeRequest

  if (customization.templateId != null) {
    return {
      ...safeRequest,
      replace_request_customization: true,
      template_id: customization.templateId,
    }
  }

  return {
    ...safeRequest,
    replace_request_customization: true,
    clear_template: true,
    extra_headers: customization.extraHeaders,
    body_override_mode: customization.bodyOverrideMode,
    body_override: customization.bodyOverride,
  }
}

export type WriteOnlyTemplateCustomization = Omit<WriteOnlyMonitorCustomization, 'templateId'>

/** Applies the same explicit replacement contract to reusable templates. */
export function applyWriteOnlyTemplateCustomization(
  request: TemplateUpdateParams,
  customization: WriteOnlyTemplateCustomization,
): TemplateUpdateParams {
  const safeRequest = { ...request }
  delete safeRequest.extra_headers
  delete safeRequest.body_override_mode
  delete safeRequest.body_override
  delete safeRequest.replace_request_customization
  if (!customization.replace) return safeRequest

  return {
    ...safeRequest,
    replace_request_customization: true,
    extra_headers: customization.extraHeaders,
    body_override_mode: customization.bodyOverrideMode,
    body_override: customization.bodyOverride,
  }
}
