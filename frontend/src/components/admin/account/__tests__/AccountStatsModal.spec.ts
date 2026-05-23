import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountStatsModal from '../AccountStatsModal.vue'

const { getStats } = vi.hoisted(() => ({
  getStats: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getStats
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('chart.js', () => ({
  Chart: { register: vi.fn() },
  CategoryScale: {},
  LinearScale: {},
  PointElement: {},
  LineElement: {},
  Title: {},
  Tooltip: {},
  Legend: {},
  Filler: {}
}))

vi.mock('vue-chartjs', () => ({
  Line: { name: 'Line', template: '<div class="line-stub"></div>' }
}))

const statsResponse = {
  history: [
    {
      date: '2026-05-18',
      label: '05/18',
      requests: 12,
      tokens: 3456,
      cost: 1.2,
      actual_cost: 1.2,
      user_cost: 0.4
    }
  ],
  summary: {
    days: 30,
    actual_days_used: 1,
    total_cost: 1.2,
    total_user_cost: 0.4,
    total_standard_cost: 1.2,
    total_requests: 12,
    total_tokens: 3456,
    avg_daily_cost: 1.2,
    avg_daily_user_cost: 0.4,
    avg_daily_requests: 12,
    avg_daily_tokens: 3456,
    avg_duration_ms: 123,
    today: {
      date: '2026-05-18',
      cost: 1.2,
      user_cost: 0.4,
      requests: 12,
      tokens: 3456
    },
    highest_cost_day: {
      date: '2026-05-18',
      label: '05/18',
      cost: 1.2,
      user_cost: 0.4,
      requests: 12
    },
    highest_request_day: {
      date: '2026-05-18',
      label: '05/18',
      requests: 12,
      cost: 1.2,
      user_cost: 0.4
    }
  },
  models: [],
  endpoints: [],
  upstream_endpoints: []
}

function mountModal(show = true) {
  return mount(AccountStatsModal, {
    props: {
      show,
      account: {
        id: 86,
        name: 'plus1',
        platform: 'openai',
        type: 'oauth',
        status: 'active'
      }
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        LoadingSpinner: { template: '<div class="loading-stub"></div>' },
        ModelDistributionChart: { template: '<div class="model-chart-stub"></div>' },
        EndpointDistributionChart: { template: '<div class="endpoint-chart-stub"></div>' },
        Icon: true
      }
    }
  })
}

describe('AccountStatsModal', () => {
  beforeEach(() => {
    getStats.mockResolvedValue(statsResponse)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('组件首次创建时 show 已为 true 也会加载账号统计', async () => {
    mountModal(true)
    await flushPromises()

    expect(getStats).toHaveBeenCalledTimes(1)
    expect(getStats).toHaveBeenCalledWith(86, 30)
  })

  it('从关闭切到打开时会加载账号统计', async () => {
    const wrapper = mountModal(false)
    await flushPromises()

    expect(getStats).not.toHaveBeenCalled()

    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(getStats).toHaveBeenCalledTimes(1)
    expect(getStats).toHaveBeenCalledWith(86, 30)
  })
})
