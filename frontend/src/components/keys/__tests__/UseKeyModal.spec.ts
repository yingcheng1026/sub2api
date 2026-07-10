import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn().mockResolvedValue(true)
  })
}))

import UseKeyModal from '../UseKeyModal.vue'

const dialogStubs = {
  BaseDialog: {
    template: '<div><slot /><slot name="footer" /></div>'
  },
  Icon: {
    template: '<span />'
  }
}

const mountModal = (platform: 'anthropic' | 'openai' | 'antigravity', overrides = {}) => mount(UseKeyModal, {
  props: {
    show: true,
    apiKey: 'sk-local-test',
    baseUrl: 'https://example.com/v1',
    platform,
    ...overrides
  },
  global: {
    stubs: dialogStubs
  }
})

const clickClientTab = async (wrapper: ReturnType<typeof mount>, labelKey: string) => {
  const tab = wrapper.findAll('button').find((button) => button.text().includes(labelKey))
  expect(tab).toBeDefined()
  await tab!.trigger('click')
  await nextTick()
}

const clickShellTab = async (wrapper: ReturnType<typeof mount>, label: string) => {
  const tab = wrapper.findAll('button').find((button) => button.text().includes(label))
  expect(tab).toBeDefined()
  await tab!.trigger('click')
  await nextTick()
}

const nativeModelAssignments = {
  ANTHROPIC_MODEL: 'gpt-5.5',
  ANTHROPIC_DEFAULT_OPUS_MODEL: 'gpt-5.5',
  ANTHROPIC_DEFAULT_SONNET_MODEL: 'gpt-5.4',
  ANTHROPIC_DEFAULT_HAIKU_MODEL: 'gpt-5.4-mini',
  CLAUDE_CODE_SUBAGENT_MODEL: 'inherit'
}

describe('UseKeyModal', () => {
  it('renders GPT-5.4 mini entry in OpenCode config', async () => {
    const wrapper = mountModal('openai')

    const opencodeTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.opencode')
    )

    expect(opencodeTab).toBeDefined()
    await opencodeTab!.trigger('click')
    await nextTick()

    const codeBlock = wrapper.find('pre code')
    expect(codeBlock.exists()).toBe(true)
    expect(codeBlock.text()).toContain('"name": "GPT-5.4 Mini"')
    expect(codeBlock.text()).not.toContain('"name": "GPT-5.4 Nano"')
  })

  it.each([
    ['macOS / Linux', 'export ', '=\"'],
    ['Windows CMD', 'set ', '='],
    ['PowerShell', '$env:', '=\"']
  ])('uses stable native GPT identities for OpenAI Claude Code on %s', async (shellLabel, prefix, separator) => {
    const wrapper = mountModal('openai', { allowMessagesDispatch: true })
    await clickClientTab(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')
    await clickShellTab(wrapper, shellLabel)

    const files = wrapper.findAll('pre code').map((code) => code.text())
    const terminal = files[0]
    const settings = JSON.parse(files[1]) as { env: Record<string, string> }

    for (const [name, model] of Object.entries(nativeModelAssignments)) {
      expect(terminal).toContain(`${prefix}${name}${separator}${model}`)
      expect(settings.env[name]).toBe(model)
    }
    expect(files.join('\n')).not.toMatch(/claude-/i)
    expect(files.join('\n')).not.toContain('gpt-5.6')
  })

  it('serializes the OpenAI Claude Code settings file as valid JSON', async () => {
    const wrapper = mountModal('openai', {
      allowMessagesDispatch: true,
      apiKey: 'sk-local-"quoted"\\tail',
      baseUrl: 'https://example.com/v1?label="quoted"'
    })
    await clickClientTab(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')

    const settingsContent = wrapper.findAll('pre code')[1].text()
    const settings = JSON.parse(settingsContent) as { env: Record<string, string> }
    expect(settings.env.ANTHROPIC_AUTH_TOKEN).toBe('sk-local-"quoted"\\tail')
    expect(settings.env.ANTHROPIC_BASE_URL).toBe('https://example.com/v1?label="quoted"')
  })

  it('keeps the Anthropic Claude Code fixture unchanged', () => {
    const wrapper = mountModal('anthropic')
    const files = wrapper.findAll('pre code').map((code) => code.text())

    expect(files[0]).toBe(`export ANTHROPIC_BASE_URL="https://example.com/v1"
export ANTHROPIC_AUTH_TOKEN="sk-local-test"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`)
    expect(files[1]).toBe(`{
  "env": {
    "ANTHROPIC_BASE_URL": "https://example.com/v1",
    "ANTHROPIC_AUTH_TOKEN": "sk-local-test",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0"
  }
}`)
  })

  it('keeps the Antigravity Claude Code fixture unchanged', () => {
    const wrapper = mountModal('antigravity')
    const files = wrapper.findAll('pre code').map((code) => code.text())

    expect(files[0]).toBe(`export ANTHROPIC_BASE_URL="https://example.com/v1/antigravity"
export ANTHROPIC_AUTH_TOKEN="sk-local-test"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`)
    expect(files.join('\n')).not.toContain('ANTHROPIC_MODEL')
  })
})
