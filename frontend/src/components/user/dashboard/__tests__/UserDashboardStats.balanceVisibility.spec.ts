import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UserDashboardStats from '../UserDashboardStats.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const stats = {
  total_api_keys: 1,
  active_api_keys: 1,
  today_requests: 0,
  total_requests: 0,
  today_actual_cost: 0,
  today_cost: 0,
  total_actual_cost: 0,
  total_cost: 0,
  today_tokens: 0,
  today_input_tokens: 0,
  today_output_tokens: 0,
  total_tokens: 0,
  total_input_tokens: 0,
  total_output_tokens: 0,
  rpm: 0,
  tpm: 0,
  average_duration_ms: 0
}

const mountStats = (hideLegacyBalance: boolean) =>
  mount(UserDashboardStats, {
    props: {
      stats,
      balance: 88,
      isSimple: false,
      hideLegacyBalance
    },
    global: {
      stubs: {
        Icon: { template: '<span />' }
      }
    }
  })

describe('UserDashboardStats legacy balance visibility', () => {
  it('does not show users.balance while a credits wallet is authoritative', () => {
    const wrapper = mountStats(true)

    expect(wrapper.find('[data-hfc-legacy-balance]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('$88.00')
  })

  it('still shows legacy balance after the caller proves no credits wallet exists', () => {
    const wrapper = mountStats(false)

    expect(wrapper.get('[data-hfc-legacy-balance]').text()).toContain('$88.00')
  })
})
