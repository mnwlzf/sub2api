import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { AdminGroup } from '@/types'
import GroupPromptBindingModal from '../GroupPromptBindingModal.vue'

const api = vi.hoisted(() => ({
  getGroupBinding: vi.fn(),
  listTemplates: vi.fn(),
  listVersions: vi.fn(),
  listAccountOverrides: vi.fn(),
  setGroupBinding: vi.fn(),
  clearAccountOverride: vi.fn(),
}))
const listAccounts = vi.hoisted(() => vi.fn())

vi.mock('@/features/prompt-templates/api', () => api)
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { list: listAccounts } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }) }))

enableAutoUnmount(afterEach)

const group = { id: 52, name: 'Group 52', platform: 'openai' } as AdminGroup

const stubs = {
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
  PlatformIcon: true,
  Select: { props: ['modelValue', 'options', 'disabled'], template: '<select />' },
  GroupPromptOverrideDialog: {
    props: ['show', 'groupId', 'accounts', 'existing', 'versionOptions', 'groupBindingDisabled'],
    template: '<div />',
  },
  ConfirmDialog: { props: ['show', 'title', 'message'], template: '<div />' },
}

function mockLoads(binding: Record<string, unknown>) {
  api.getGroupBinding.mockResolvedValue(binding)
  api.listTemplates.mockResolvedValue([{ id: 3, name: 'Template A', archived_at: null }])
  api.listVersions.mockResolvedValue([{ id: 10, template_id: 3, version_no: 2 }])
  api.listAccountOverrides.mockResolvedValue([])
  listAccounts.mockResolvedValue({ items: [{ id: 7, name: 'account-7' }] })
}

async function mountModal(binding: Record<string, unknown>) {
  mockLoads(binding)
  const wrapper = mount(GroupPromptBindingModal, {
    props: { show: false, group },
    global: { stubs },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  api.setGroupBinding.mockResolvedValue({
    group_id: 52,
    mode: 'disabled',
    version_id: null,
    revision: 4,
  })
  api.clearAccountOverride.mockResolvedValue(undefined)
})

describe('GroupPromptBindingModal', () => {
  it('显示已启用状态和固定的追加注入语义', async () => {
    const wrapper = await mountModal({ group_id: 52, mode: 'version', version_id: 10, revision: 3 })

    const status = wrapper.get('[data-test="binding-status"]')
    expect(status.text()).toContain('admin.promptTemplates.binding.statusBound')

    const injection = wrapper.get('[data-test="injection-mode"]')
    expect(injection.text()).toContain('admin.promptTemplates.binding.injectionModeTitle')
    expect(injection.text()).toContain('admin.promptTemplates.binding.injectionModeDetail')
  })

  it('不再提供与模式切换重复的独立解绑按钮', async () => {
    const wrapper = await mountModal({ group_id: 52, mode: 'version', version_id: 10, revision: 3 })
    expect(wrapper.find('[data-test="binding-disable"]').exists()).toBe(false)
  })

  it('切到已禁用并保存时显式提交 version_id: null', async () => {
    const wrapper = await mountModal({ group_id: 52, mode: 'version', version_id: 10, revision: 3 })

    await wrapper.get('[data-test="binding-mode-disabled"]').trigger('click')
    const save = wrapper.get('[data-test="binding-save"]')
    expect(save.attributes('disabled')).toBeUndefined()
    await save.trigger('click')
    await flushPromises()

    expect(api.setGroupBinding).toHaveBeenCalledWith(52, {
      mode: 'disabled',
      version_id: null,
      revision: 3,
    })
  })

  it('状态行反映已保存状态，未保存的编辑不会提前改写它', async () => {
    const wrapper = await mountModal({ group_id: 52, mode: 'version', version_id: 10, revision: 3 })
    await wrapper.get('[data-test="binding-mode-disabled"]').trigger('click')

    // 只是编辑，尚未保存：仍应显示“已启用”。
    expect(wrapper.get('[data-test="binding-status"]').text()).toContain(
      'admin.promptTemplates.binding.statusBound',
    )
    expect(api.setGroupBinding).not.toHaveBeenCalled()
  })

  it('未启用分组显示禁用状态与覆盖失效提示', async () => {
    const wrapper = await mountModal({ group_id: 52, mode: 'disabled', version_id: null, revision: 0 })

    expect(wrapper.get('[data-test="binding-status"]').text()).toContain(
      'admin.promptTemplates.binding.statusDisabled',
    )
    expect(wrapper.text()).toContain('admin.promptTemplates.binding.groupDisabledOverridesNotice')
  })
})
