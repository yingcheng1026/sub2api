import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminUser, Group } from '@/types'
import UserAllowedGroupsModal from '../UserAllowedGroupsModal.vue'

const { listGroups, getUserById, updateUser, showError, showSuccess } = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getUserById: vi.fn(),
  updateUser: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { list: listGroups },
    users: { getById: getUserById, update: updateUser }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const BaseDialogStub = {
  props: ['show', 'title'],
  template: '<section v-if="show"><h1>{{ title }}</h1><slot /><footer><slot name="footer" /></footer></section>'
}

const groups: Group[] = [
  {
    id: 3,
    name: 'openai-default',
    description: 'GPT wallet default',
    platform: 'openai',
    rate_multiplier: 1,
    is_exclusive: false,
    status: 'active',
    subscription_type: 'standard'
  } as Group,
  {
    id: 22,
    name: 'vip',
    description: 'Claude dedicated',
    platform: 'anthropic',
    rate_multiplier: 1,
    is_exclusive: true,
    status: 'active',
    subscription_type: 'standard'
  } as Group,
  {
    id: 99,
    name: 'other-exclusive',
    description: 'Another dedicated group',
    platform: 'openai',
    rate_multiplier: 1,
    is_exclusive: true,
    status: 'active',
    subscription_type: 'standard'
  } as Group
]

function user(id: number, allowedGroups: number[] = []): AdminUser {
  return {
    id,
    email: `user-${id}@example.test`,
    username: `user-${id}`,
    role: 'user',
    balance: 0,
    concurrency: 1,
    status: 'active',
    allowed_groups: allowedGroups,
    group_rates: {},
    created_at: '2026-07-12T00:00:00Z',
    updated_at: '2026-07-12T00:00:00Z'
  } as AdminUser
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function mountModal(currentUser: AdminUser) {
  return mount(UserAllowedGroupsModal, {
    props: { show: true, user: currentUser },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        PlatformIcon: { template: '<span />' }
      }
    }
  })
}

describe('UserAllowedGroupsModal', () => {
  beforeEach(() => {
    listGroups.mockReset().mockResolvedValue({ items: groups })
    getUserById.mockReset()
    updateUser.mockReset().mockResolvedValue(user(1))
    showError.mockReset()
    showSuccess.mockReset()
  })

  it('loads when initially open and explains the exact wallet routing policy', async () => {
    getUserById.mockResolvedValue(user(1))
    const wrapper = mountModal(user(1))
    await flushPromises()

    expect(listGroups).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('admin.users.walletRoutingBusinessRule')
    expect(wrapper.text()).toContain('admin.users.openAIDefaultGroupHint')
    expect(wrapper.text()).toContain('admin.users.vipGroupHint')
  })

  it('clears the previous user state and disables save while the next user loads', async () => {
    getUserById
      .mockResolvedValueOnce(user(1, [22]))
      .mockResolvedValueOnce(user(2))
    const nextLoad = deferred<{ items: Group[] }>()
    listGroups
      .mockResolvedValueOnce({ items: groups })
      .mockReturnValueOnce(nextLoad.promise)

    const wrapper = mountModal(user(1, [22]))
    await flushPromises()
    expect(wrapper.text()).toContain('vip')

    await wrapper.setProps({ user: user(2) })

    expect(wrapper.text()).not.toContain('other-exclusive')
    expect(wrapper.get('[data-test="save-groups"]').attributes('disabled')).toBeDefined()
    expect(updateUser).not.toHaveBeenCalled()

    nextLoad.reject(new Error('group load failed'))
    await flushPromises()
    expect(wrapper.get('[data-test="group-load-error"]').text()).toContain(
      'admin.users.groupConfigLoadFailed'
    )
    expect(wrapper.get('[data-test="save-groups"]').attributes('disabled')).toBeDefined()
  })

  it('ignores an older request that resolves after a newer user request', async () => {
    const firstLoad = deferred<{ items: Group[] }>()
    listGroups
      .mockReturnValueOnce(firstLoad.promise)
      .mockResolvedValueOnce({ items: groups })
    getUserById
      .mockResolvedValueOnce(user(1, [22]))
      .mockResolvedValueOnce(user(2))

    const wrapper = mountModal(user(1, [22]))
    await wrapper.setProps({ user: user(2) })
    await flushPromises()

    firstLoad.resolve({ items: [{ ...groups[1], name: 'stale-vip' } as Group] })
    await flushPromises()

    expect(wrapper.text()).toContain('user-2@example.test')
    expect(wrapper.text()).toContain('vip')
    expect(wrapper.text()).not.toContain('stale-vip')
  })

  it('refuses to overwrite group grants changed after the modal was opened', async () => {
    const original = user(1)
    getUserById
      .mockResolvedValueOnce(original)
      .mockResolvedValueOnce(user(1, [99]))
    const wrapper = mountModal(original)
    await flushPromises()

    const vipCheckbox = wrapper.findAll('input[type="checkbox"]')[0]
    await vipCheckbox.setValue(true)
    await wrapper.get('[data-test="save-groups"]').trigger('click')
    await flushPromises()

    expect(getUserById).toHaveBeenCalledWith(1)
    expect(updateUser).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.users.groupConfigStale')
  })
})
