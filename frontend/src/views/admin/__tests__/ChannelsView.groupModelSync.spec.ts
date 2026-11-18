import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  BUILTIN_PLATFORM_CATALOG,
  resetPlatformCatalog,
  setPlatformCatalog
} from '@/constants/platformCatalog'
import ChannelsView from '@/views/admin/ChannelsView.vue'

const { listChannels, getGroups, getAvailableModels, showSuccess, showError } = vi.hoisted(() => ({
  listChannels: vi.fn(),
  getGroups: vi.fn(),
  getAvailableModels: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: { list: listChannels, syncPricingModels: vi.fn() },
    groups: { getAll: getGroups, getAvailableModels },
    settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false }) }
  }
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess, showError }) }))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

function mountView() {
  return mount(ChannelsView, {
    global: {
      stubs: {
        AppLayout: defineComponent({ template: '<main><slot /></main>' }),
        TablePageLayout: defineComponent({
          template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>'
        }),
        BaseDialog: defineComponent({
          props: ['show'],
          template: '<section v-if="show"><slot /><slot name="footer" /></section>'
        }),
        PricingEntryCard: defineComponent({
          props: ['entry'],
          template: '<div class="pricing-entry">{{ entry.models.join(", ") }}</div>'
        }),
        DataTable: true,
        Pagination: true,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        Toggle: true
      }
    }
  })
}

async function openDialog(wrapper: ReturnType<typeof mountView>) {
  await flushPromises()
  await wrapper.findAll('button').find(button => button.text() === 'admin.channels.createChannel')!.trigger('click')
  await flushPromises()
}

async function enablePlatform(wrapper: ReturnType<typeof mountView>, platform: string) {
  const label = wrapper.findAll('label').find(label => label.text() === `admin.groups.platforms.${platform}`)
  expect(label).toBeDefined()
  await label!.get('input[type="checkbox"]').setValue(true)
  await wrapper.findAll('button.channel-tab').find(button => button.text() === `admin.groups.platforms.${platform}`)!.trigger('click')
}

/** 点击平台分组列表里指定分组的复选框。 */
async function selectGroup(wrapper: ReturnType<typeof mountView>, groupName: string) {
  const label = wrapper.findAll('label').find(label => label.text().includes(groupName))
  expect(label, `group label ${groupName}`).toBeDefined()
  await label!.get('input[type="checkbox"]').setValue(true)
}

function syncButton(wrapper: ReturnType<typeof mountView>) {
  return wrapper.get('[data-testid="sync-group-models"]')
}

describe('ChannelsView group model sync', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    listChannels.mockResolvedValue({ items: [], total: 0 })
    getGroups.mockResolvedValue([
      { id: 7, name: 'openai-group', platform: 'openai', rate_multiplier: 1, account_count: 3 }
    ])
    getAvailableModels.mockReset()
  })

  afterEach(() => {
    resetPlatformCatalog()
  })

  it('stays disabled until the section has a group', async () => {
    const wrapper = mountView()
    await openDialog(wrapper)
    await enablePlatform(wrapper, 'openai')

    expect(syncButton(wrapper).attributes('disabled')).toBeDefined()

    await selectGroup(wrapper, 'openai-group')
    expect(syncButton(wrapper).attributes('disabled')).toBeUndefined()
    expect(getAvailableModels).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('adds the selected groups\' models as one new pricing entry', async () => {
    getAvailableModels.mockResolvedValue({
      models: ['gpt-5.5', 'gpt-6.2-sol'],
      account_count: 3,
      unrestricted_accounts: 0
    })
    const wrapper = mountView()
    await openDialog(wrapper)
    await enablePlatform(wrapper, 'openai')
    await selectGroup(wrapper, 'openai-group')

    await syncButton(wrapper).trigger('click')
    await flushPromises()

    expect(getAvailableModels).toHaveBeenCalledOnce()
    expect(getAvailableModels).toHaveBeenCalledWith([7], 'openai')
    expect(wrapper.findAll('.pricing-entry')).toHaveLength(1)
    expect(wrapper.get('.pricing-entry').text()).toBe('gpt-5.5, gpt-6.2-sol')
    expect(showSuccess).toHaveBeenCalledWith('admin.channels.form.syncGroupModelsSuccess')
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('skips models that are already configured and reports an up-to-date list', async () => {
    getAvailableModels.mockResolvedValueOnce({
      models: ['gpt-5.5', 'gpt-6.2-sol'],
      account_count: 2,
      unrestricted_accounts: 0
    })
    getAvailableModels.mockResolvedValueOnce({
      models: ['gpt-5.5', 'gpt-6.2-sol'],
      account_count: 2,
      unrestricted_accounts: 0
    })
    const wrapper = mountView()
    await openDialog(wrapper)
    await enablePlatform(wrapper, 'openai')
    await selectGroup(wrapper, 'openai-group')

    await syncButton(wrapper).trigger('click')
    await flushPromises()
    await syncButton(wrapper).trigger('click')
    await flushPromises()

    expect(wrapper.findAll('.pricing-entry')).toHaveLength(1)
    expect(showSuccess).toHaveBeenLastCalledWith('admin.channels.form.syncGroupModelsAlreadyUpToDate')
    wrapper.unmount()
  })

  it('warns when some accounts have no model restriction', async () => {
    getAvailableModels.mockResolvedValue({
      models: ['gpt-5.5'],
      account_count: 4,
      unrestricted_accounts: 2
    })
    const wrapper = mountView()
    await openDialog(wrapper)
    await enablePlatform(wrapper, 'openai')
    await selectGroup(wrapper, 'openai-group')

    await syncButton(wrapper).trigger('click')
    await flushPromises()

    expect(showSuccess).toHaveBeenCalledWith('admin.channels.form.syncGroupModelsSuccessUnrestricted')
    wrapper.unmount()
  })

  it('surfaces backend failures without adding an entry', async () => {
    getAvailableModels.mockRejectedValue(new Error('boom'))
    const wrapper = mountView()
    await openDialog(wrapper)
    await enablePlatform(wrapper, 'openai')
    await selectGroup(wrapper, 'openai-group')

    await syncButton(wrapper).trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalled()
    expect(wrapper.findAll('.pricing-entry')).toHaveLength(0)
    wrapper.unmount()
  })
})
