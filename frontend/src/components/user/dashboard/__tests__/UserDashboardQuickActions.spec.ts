import { mount } from '@vue/test-utils'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import UserDashboardQuickActions from '../UserDashboardQuickActions.vue'

const { createChatBridgeCode, routerPush } = vi.hoisted(() => ({
  createChatBridgeCode: vi.fn(),
  routerPush: vi.fn()
}))

vi.mock('@/api', () => ({
  authAPI: {
    createChatBridgeCode
  }
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: routerPush
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) =>
      ({
        'dashboard.quickActions': '快捷操作',
        'dashboard.openChat': '网页 AI',
        'dashboard.openChatHint': '聊天和生图都可以直接打开',
        'dashboard.chatModeLabel': '选择入口',
        'dashboard.chatModeChat': '聊天',
        'dashboard.chatModeImage': '生图',
        'dashboard.chatModelLabel': '选择聊天模型',
        'dashboard.imageModelLabel': '选择生图模型',
        'dashboard.chatOpenButton': '打开',
        'dashboard.chatOpening': '打开中',
        'dashboard.chatModels.gpt55': '默认推荐，综合体验最好',
        'dashboard.chatModels.claudeOpus47': '适合复杂推理和长文写作',
        'dashboard.chatModels.claudeSonnet46': '适合日常工作，稳定均衡',
        'dashboard.chatModels.gemini31Pro': '适合长内容和多模态任务',
        'dashboard.chatModels.gpt54Mini': '速度快，成本更低',
        'dashboard.imageModels.gptImage2': '默认推荐，画质最好',
        'dashboard.imageModels.gptImage15': '速度快，适合反复改图',
        'dashboard.imageModels.gptImage1': '稳定通用',
        'dashboard.imageModels.dalle3': '经典文生图',
        'dashboard.createApiKey': '创建 API 密钥',
        'dashboard.generateNewKey': '生成新的 API 密钥',
        'dashboard.viewUsage': '查看使用记录',
        'dashboard.checkDetailedLogs': '查看详细的使用日志',
        'dashboard.redeemCode': '兑换码',
        'dashboard.addBalanceWithCode': '使用兑换码充值'
      })[key] || key
  })
}))

const mountQuickActions = () =>
  mount(UserDashboardQuickActions, {
    global: {
      stubs: {
        Icon: {
          props: ['name', 'size'],
          template: '<span />'
        }
      }
    }
  })

describe('UserDashboardQuickActions', () => {
  beforeEach(() => {
    createChatBridgeCode.mockReset()
    routerPush.mockReset()
  })

  it('renders visible top model choices in the chat quick action', () => {
    const wrapper = mountQuickActions()
    const options = wrapper.findAll('#launch-model-select option').map((option) => option.text())

    expect(options).toEqual(
      expect.arrayContaining([
        expect.stringContaining('GPT-5.5'),
        expect.stringContaining('Claude Opus 4.7'),
        expect.stringContaining('Claude Sonnet 4.6'),
        expect.stringContaining('Gemini 3.1 Pro'),
        expect.stringContaining('GPT-5.4 Mini')
      ])
    )
  })

  it('renders image generation as a direct launch mode with image models', async () => {
    const wrapper = mountQuickActions()

    await wrapper.get('[data-test-id="launch-mode-image"]').trigger('click')

    const options = wrapper.findAll('#launch-model-select option').map((option) => option.text())
    expect(options).toEqual(
      expect.arrayContaining([
        expect.stringContaining('GPT Image 2'),
        expect.stringContaining('GPT Image 1.5'),
        expect.stringContaining('DALL·E 3')
      ])
    )
  })

  it('opens chat with the selected model in the launch path', async () => {
    createChatBridgeCode.mockReturnValue(new Promise(() => undefined))

    const wrapper = mountQuickActions()
    await wrapper.get('#launch-model-select').setValue('claude-opus-4-7')
    await wrapper.findAll('button').find((button) => button.text().includes('打开'))?.trigger('click')

    expect(createChatBridgeCode).toHaveBeenCalledWith({
      redirect_path: '/?hfc_model=claude-opus-4-7&hfc_provider=openai'
    })
  })

  it('opens image generation with the selected image model in the launch path', async () => {
    createChatBridgeCode.mockReturnValue(new Promise(() => undefined))

    const wrapper = mountQuickActions()
    await wrapper.get('[data-test-id="launch-mode-image"]').trigger('click')
    await wrapper.get('#launch-model-select').setValue('gpt-image-1.5')
    await wrapper.findAll('button').find((button) => button.text().includes('打开'))?.trigger('click')

    expect(createChatBridgeCode).toHaveBeenCalledWith({
      redirect_path:
        '/?hfc_launch=image&hfc_image_model=gpt-image-1.5&hfc_image_provider=openai'
    })
  })
})
