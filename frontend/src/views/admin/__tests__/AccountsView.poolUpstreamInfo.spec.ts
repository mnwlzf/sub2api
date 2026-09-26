import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getById,
  getBatchTodayStats,
  getUpstreamBillingProbeSettings,
  getUpstreamBillingRatesWithEtag,
  probePoolUpstreamInfo,
  getAllProxies,
  getAllGroups,
  showError
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getById: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  getUpstreamBillingRatesWithEtag: vi.fn(),
  probePoolUpstreamInfo: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      getById,
      listWithEtag,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings,
      getUpstreamBillingRatesWithEtag,
      probePoolUpstreamInfo,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn(),
      refreshCredentials: vi.fn()
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showWarning: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${Object.values(params).join(',')}` : key
    })
  }
})

const HelpTooltipStub = defineComponent({
  props: { widthClass: { type: String, default: '' } },
  template: '<span><slot name="trigger" /><slot /></span>'
})

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-account-id="row.id">
        <slot name="cell-pool_upstream_info" :row="row" />
      </div>
    </div>
  `
})

function mountView() {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountTableActions: { template: '<div><slot name="after" /></div>' },
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        UpstreamBillingRateCell: true,
        HelpTooltip: HelpTooltipStub,
        Icon: true,
        Teleport: true
      }
    }
  })
}

const poolRow = {
  id: 42,
  name: 'pool row',
  platform: 'openai',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  concurrency: 2,
  priority: 1,
  group_ids: [],
  credentials: { pool_mode: true },
  extra: {
    pool_upstream_platform: 'chatgpt2api',
    pool_upstream_features: ['account_count', 'image_quota']
  }
}

const snapshot = {
  status: 'ok',
  platform: 'chatgpt2api',
  features: ['account_count', 'image_quota'],
  data: { accounts_active: 1, total_quota: 25 },
  last_attempt_at: '2026-09-27T00:00:00Z',
  next_probe_at: '2026-09-27T00:30:00Z',
  received_at: '2026-09-27T00:00:00Z',
  fresh_until: '2026-09-27T01:00:00Z',
  http_status: 200
}

describe('admin AccountsView pool upstream info column', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({ items: [poolRow], total: 1, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: 'list-etag', data: null })
    getById.mockReset().mockResolvedValue(poolRow)
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getUpstreamBillingProbeSettings.mockReset().mockResolvedValue({ enabled: true })
    getUpstreamBillingRatesWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: 'rates-etag', data: null })
    probePoolUpstreamInfo.mockReset()
    getAllProxies.mockReset().mockResolvedValue([])
    getAllGroups.mockReset().mockResolvedValue([])
    showError.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('renders a dash for opted-in accounts that were never probed', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.notProbed'
    )
    wrapper.unmount()
  })

  it('renders a dash instead of the probe action for unconfigured accounts', async () => {
    listAccounts.mockResolvedValue({
      items: [{ ...poolRow, id: 43, type: 'apikey', extra: {} }],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="pool-upstream-info-empty"]').text()).toBe('-')
    expect(wrapper.find('[data-testid="pool-upstream-info-probe"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('runs the manual refresh and merges the returned snapshot plus the projection refresh', async () => {
    probePoolUpstreamInfo.mockResolvedValue({ account_id: 42, snapshot })
    getUpstreamBillingRatesWithEtag.mockResolvedValue({
      notModified: false,
      etag: 'rates-etag-2',
      data: {
        items: [{ account_id: 42, snapshot: null, pool_upstream_info: snapshot }],
        total: 1,
        page: 1,
        page_size: 20
      }
    })

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="pool-upstream-info-probe"]').trigger('click')
    await flushPromises()

    expect(probePoolUpstreamInfo).toHaveBeenCalledWith(42)
    expect(getUpstreamBillingRatesWithEtag).toHaveBeenCalled()
    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.accountsActive:1 · admin.accounts.poolUpstream.totalQuota:25'
    )
    wrapper.unmount()
  })

  it('surfaces a safe error when the manual refresh fails', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    probePoolUpstreamInfo.mockRejectedValue(new Error('network down'))

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="pool-upstream-info-probe"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('network down')
    wrapper.unmount()
    consoleError.mockRestore()
  })
})
