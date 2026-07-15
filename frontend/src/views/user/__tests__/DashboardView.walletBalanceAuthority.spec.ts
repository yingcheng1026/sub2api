import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import DashboardView from '../DashboardView.vue'

const mocks = vi.hoisted(() => ({
  getActiveSubscriptions: vi.fn(),
  refreshUser: vi.fn(),
  getDashboardStats: vi.fn(),
  getDashboardTrend: vi.fn(),
  getDashboardModels: vi.fn(),
  getByDateRange: vi.fn()
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { balance: 88 },
    isSimpleMode: false,
    refreshUser: mocks.refreshUser
  })
}))

vi.mock('@/api/subscriptions', () => ({
  default: { getActiveSubscriptions: mocks.getActiveSubscriptions }
}))

vi.mock('@/api/usage', () => ({
  usageAPI: {
    getDashboardStats: mocks.getDashboardStats,
    getDashboardTrend: mocks.getDashboardTrend,
    getDashboardModels: mocks.getDashboardModels,
    getByDateRange: mocks.getByDateRange
  }
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const mountDashboard = () =>
  mount(DashboardView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: { template: '<span />' },
        WalletBalanceCard: { template: '<div data-wallet-card />' },
        WalletModelRouteList: { template: '<div />' },
        RenewLiandongModal: { template: '<div />' },
        UserDashboardStats: {
          props: ['hideLegacyBalance'],
          template: '<div data-dashboard-stats :data-hide-legacy-balance="String(hideLegacyBalance)" />'
        },
        UserDashboardCharts: { template: '<div />' },
        UserDashboardRecentUsage: { template: '<div />' },
        UserDashboardQuickActions: { template: '<div />' }
      }
    }
  })

describe('DashboardView wallet balance authority', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.refreshUser.mockResolvedValue(undefined)
    mocks.getDashboardStats.mockResolvedValue({})
    mocks.getDashboardTrend.mockResolvedValue({ trend: [] })
    mocks.getDashboardModels.mockResolvedValue({ models: [] })
    mocks.getByDateRange.mockResolvedValue({ items: [] })
  })

  it('hides legacy balance and shows a warning when wallet authority cannot be verified', async () => {
    mocks.getActiveSubscriptions.mockRejectedValue(new Error('wallet lookup unavailable'))

    const wrapper = mountDashboard()
    await flushPromises()

    expect(wrapper.get('[data-dashboard-stats]').attributes('data-hide-legacy-balance')).toBe('true')
    expect(wrapper.get('[data-hfc-wallet-status-unavailable]').text()).toContain(
      'dashboard.walletStatusUnavailable'
    )
  })

  it('keeps legacy balance hidden when an active permanent credits wallet exists', async () => {
    mocks.getActiveSubscriptions.mockResolvedValue([{
      id: 9,
      user_id: 1,
      group_id: null,
      status: 'active',
      wallet_balance_usd: 50,
      wallet_initial_usd: 100,
      expires_at: '2099-12-31T23:59:59Z'
    }])

    const wrapper = mountDashboard()
    await flushPromises()

    expect(wrapper.get('[data-dashboard-stats]').attributes('data-hide-legacy-balance')).toBe('true')
    expect(wrapper.find('[data-hfc-wallet-status-unavailable]').exists()).toBe(false)
    expect(wrapper.find('[data-wallet-card]').exists()).toBe(true)
  })
})
