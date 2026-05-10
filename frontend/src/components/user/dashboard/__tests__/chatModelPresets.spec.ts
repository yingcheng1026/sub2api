import { describe, expect, it } from 'vitest'

import {
  buildImageFallbackURL,
  buildImageRedirectPath,
  buildChatFallbackURL,
  buildChatRedirectPath,
  chatModelChoices,
  defaultChatModelChoice,
  defaultImageModelChoice,
  imageModelChoices
} from '../chatModelPresets'

describe('chat model presets', () => {
  it('offers visible top model choices for non-technical users', () => {
    expect(defaultChatModelChoice.model).toBe('gpt-5.5')
    expect(chatModelChoices.map((choice) => choice.model)).toEqual(
      expect.arrayContaining([
        'gpt-5.5',
        'claude-opus-4-7',
        'claude-sonnet-4-6',
        'gemini-3.1-pro-preview',
        'gpt-5.4-mini'
      ])
    )
  })

  it('builds a chat redirect path with the selected model and provider', () => {
    const path = buildChatRedirectPath({
      label: 'Claude Opus 4.7',
      model: 'claude-opus-4-7',
      provider: 'openai',
      tagline: 'Deep reasoning'
    })

    expect(path).toBe('/?hfc_model=claude-opus-4-7&hfc_provider=openai')
  })

  it('keeps the selected model when falling back to a locally built chat URL', () => {
    const url = new URL(
      buildChatFallbackURL('code-123', {
        label: 'Gemini 3.1 Pro',
        model: 'gemini-3.1-pro-preview',
        provider: 'openai',
        tagline: 'Long context'
      })
    )

    expect(url.origin).toBe('https://chat.handsfreeclub.com')
    expect(url.pathname).toBe('/')
    expect(url.searchParams.get('hfc_chat_code')).toBe('code-123')
    expect(url.searchParams.get('hfc_model')).toBe('gemini-3.1-pro-preview')
    expect(url.searchParams.get('hfc_provider')).toBe('openai')
  })

  it('offers a direct image generation launch with image models', () => {
    expect(defaultImageModelChoice.model).toBe('gpt-image-2')
    expect(imageModelChoices.map((choice) => choice.model)).toEqual(
      expect.arrayContaining(['gpt-image-2', 'gpt-image-1.5', 'gpt-image-1', 'dall-e-3'])
    )
  })

  it('builds an image redirect path with the selected image model', () => {
    const path = buildImageRedirectPath({
      label: 'GPT Image 2',
      model: 'gpt-image-2',
      provider: 'openai',
      tagline: 'Best image quality'
    })

    expect(path).toBe('/?hfc_launch=image&hfc_image_model=gpt-image-2&hfc_image_provider=openai')
  })

  it('keeps the selected image model when falling back to a locally built image URL', () => {
    const url = new URL(
      buildImageFallbackURL('code-123', {
        label: 'GPT Image 1.5',
        model: 'gpt-image-1.5',
        provider: 'openai',
        tagline: 'Fast edits'
      })
    )

    expect(url.origin).toBe('https://chat.handsfreeclub.com')
    expect(url.pathname).toBe('/')
    expect(url.searchParams.get('hfc_chat_code')).toBe('code-123')
    expect(url.searchParams.get('hfc_launch')).toBe('image')
    expect(url.searchParams.get('hfc_image_model')).toBe('gpt-image-1.5')
    expect(url.searchParams.get('hfc_image_provider')).toBe('openai')
  })
})
