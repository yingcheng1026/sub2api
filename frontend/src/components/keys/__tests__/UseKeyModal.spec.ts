import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { execFileSync } from 'node:child_process'

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
  ANTHROPIC_MODEL: 'gpt-5.6-terra',
  ANTHROPIC_CUSTOM_MODEL_OPTION: 'gpt-5.6-terra',
  ANTHROPIC_DEFAULT_OPUS_MODEL: 'gpt-5.6-sol',
  ANTHROPIC_DEFAULT_SONNET_MODEL: 'gpt-5.6-terra',
  ANTHROPIC_DEFAULT_HAIKU_MODEL: 'gpt-5.6-luna',
  CLAUDE_CODE_SUBAGENT_MODEL: 'inherit'
}

describe('UseKeyModal', () => {
  it('renders exact GPT-5.6 tiers in OpenCode config', async () => {
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
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Sol"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Terra"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Luna"')
    expect(codeBlock.text()).not.toContain('"name": "GPT-5.4 Nano"')
  })

  it('uses GPT-5.6 Terra over HTTP Responses in the default Codex config', () => {
    const wrapper = mountModal('openai')
    const config = wrapper.findAll('pre code')[0].text()

    expect(config).toContain('model = "gpt-5.6-terra"')
    expect(config).toContain('review_model = "gpt-5.6-terra"')
    expect(config).toContain('wire_api = "responses"')
    expect(config).not.toContain('supports_websockets = true')
  })

  it('keeps the WebSocket template on its validated GPT-5.4 boundary', async () => {
    const wrapper = mountModal('openai')
    await clickClientTab(wrapper, 'keys.useKeyModal.cliTabs.codexCliWs')
    const config = wrapper.findAll('pre code')[0].text()

    expect(config).toContain('model = "gpt-5.4"')
    expect(config).toContain('review_model = "gpt-5.4"')
    expect(config).toContain('supports_websockets = true')
    expect(config).not.toContain('gpt-5.6')
  })

  it.each([
    'macOS / Linux',
    'Windows CMD',
    'PowerShell'
  ])('uses stable native GPT identities for OpenAI Claude Code on %s', async (shellLabel) => {
    const wrapper = mountModal('openai', { allowMessagesDispatch: true })
    await clickClientTab(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')
    await clickShellTab(wrapper, shellLabel)

    const files = wrapper.findAll('pre code').map((code) => code.text())
    const terminal = files[0]
    const settings = JSON.parse(files[1]) as { env: Record<string, string> }

    for (const [name, model] of Object.entries(nativeModelAssignments)) {
      if (shellLabel === 'macOS / Linux') {
        expect(terminal).toContain(`export ${name}='${model}'`)
      } else if (shellLabel === 'PowerShell') {
        expect(terminal).toContain(`$env:${name}='${model}'`)
      } else {
        expect(terminal).toContain(`set "${name}=${model}"`)
      }
      expect(settings.env[name]).toBe(model)
    }
    expect(files.join('\n')).not.toMatch(/claude-/i)
    expect(files.join('\n')).toContain('gpt-5.6-terra')
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

  it('shell-encodes hostile OpenAI Claude Code values and fails closed for CMD', async () => {
    const hostile = '$(printf INJECTED)`printf INJECTED`&|\'"%VAR%!\r\nnext'
    const hostileBaseUrl = `https://example.com/v1/${hostile}`
    const hostileApiKey = `sk-local-${hostile}`
    const wrapper = mountModal('openai', {
      allowMessagesDispatch: true,
      apiKey: hostileApiKey,
      baseUrl: hostileBaseUrl
    })
    await clickClientTab(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')

    const posixTerminal = wrapper.findAll('pre code')[0].text()
    const posixQuote = (value: string) => `'${value.replace(/'/g, "'\"'\"'")}'`
    expect(posixTerminal).toContain(`export ANTHROPIC_BASE_URL=${posixQuote(hostileBaseUrl)}`)
    expect(posixTerminal).toContain(`export ANTHROPIC_AUTH_TOKEN=${posixQuote(hostileApiKey)}`)
    if (process.platform !== 'win32') {
      expect(() => execFileSync('/bin/sh', ['-eu', '-c', `${posixTerminal}
test "$ANTHROPIC_BASE_URL" = "$EXPECTED_BASE_URL"
test "$ANTHROPIC_AUTH_TOKEN" = "$EXPECTED_AUTH_TOKEN"`], {
        env: {
          ...process.env,
          EXPECTED_BASE_URL: hostileBaseUrl,
          EXPECTED_AUTH_TOKEN: hostileApiKey
        }
      })).not.toThrow()
    }

    await clickShellTab(wrapper, 'PowerShell')
    const powerShellTerminal = wrapper.findAll('pre code')[0].text()
    const powerShellQuote = (value: string) => `'${value.replace(/'/g, "''")}'`
    expect(powerShellTerminal).toContain(`$env:ANTHROPIC_BASE_URL=${powerShellQuote(hostileBaseUrl)}`)
    expect(powerShellTerminal).toContain(`$env:ANTHROPIC_AUTH_TOKEN=${powerShellQuote(hostileApiKey)}`)

    await clickShellTab(wrapper, 'Windows CMD')
    const files = wrapper.findAll('pre code').map((code) => code.text())
    const cmdTerminal = files[0]
    expect(cmdTerminal).toContain('Unsafe value omitted')
    expect(cmdTerminal).toContain('settings.json')
    expect(cmdTerminal).not.toContain(hostileBaseUrl)
    expect(cmdTerminal).not.toContain(hostileApiKey)
    expect(cmdTerminal).not.toContain('%VAR%')
    expect(cmdTerminal).not.toContain('\r')
    expect(cmdTerminal).not.toContain('next')

    const settings = JSON.parse(files[1]) as { env: Record<string, string> }
    expect(settings.env.ANTHROPIC_BASE_URL).toBe(hostileBaseUrl)
    expect(settings.env.ANTHROPIC_AUTH_TOKEN).toBe(hostileApiKey)
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
