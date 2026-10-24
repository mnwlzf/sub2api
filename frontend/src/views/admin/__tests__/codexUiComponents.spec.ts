import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import CodexTicketStatusCell from '@/components/admin/codex/CodexTicketStatusCell.vue'
import CodexDegradationModal from '@/components/admin/codex/CodexDegradationModal.vue'
import { __resetCodexTicketStatusesCacheForTests } from '@/features/codexTickets/useCodexTicketStatuses'
import type { Account } from '@/types'
import type { CodexTicketStatus } from '@/api/admin/codexTicket'

const {
  getAccountTicketStatuses,
  listDegradationModels,
  getDegradationSettings,
  listAccountDegradationResults,
  diagnoseAccount
} = vi.hoisted(() => ({
  getAccountTicketStatuses: vi.fn(),
  listDegradationModels: vi.fn(),
  getDegradationSettings: vi.fn(),
  listAccountDegradationResults: vi.fn(),
  diagnoseAccount: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    codexTicket: {
      getAccountTicketStatuses
    },
    codexDegradation: {
      listDegradationModels,
      getDegradationSettings,
      listAccountDegradationResults,
      diagnoseAccount
    }
  }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key} ${JSON.stringify(params)}` : key,
    te: () => true
  })
}))

const BaseDialogStub = {
  name: 'BaseDialog',
  props: ['show', 'title', 'width', 'closeOnClickOutside', 'zIndex'],
  emits: ['close'],
  template: '<div v-if="show" data-test="dialog"><slot /><slot name="footer" /></div>'
}

function makeAccount(overrides: Partial<Account> = {}): Account {
  return {
    id: 7,
    name: 'openai-oauth-7',
    platform: 'openai',
    type: 'oauth',
    parent_account_id: null,
    extra: {},
    ...overrides
  } as Account
}

function makeTicketStatus(overrides: Partial<CodexTicketStatus> = {}): CodexTicketStatus {
  return {
    model: 'gpt-6.1-sol',
    length: 292,
    turn_state_present: true,
    cookie_present: true,
    ready: true,
    blocked: false,
    remaining_seconds: 1800,
    expected_ticket_length: 292,
    harvest_enabled: true,
    harvest_paused: false,
    participation_enabled: true,
    ...overrides
  }
}

describe('CodexTicketStatusCell', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    __resetCodexTicketStatusesCacheForTests()
  })

  it('renders per-model ticket states for eligible accounts', async () => {
    getAccountTicketStatuses.mockResolvedValue({
      account_id: 7,
      items: [
        makeTicketStatus(),
        makeTicketStatus({ model: 'gpt-6-astra', ready: false, remaining_seconds: 0 })
      ],
      manual_available: true,
      manual_unavailable_reason: ''
    })

    const wrapper = mount(CodexTicketStatusCell, { props: { account: makeAccount() } })
    await flushPromises()

    expect(getAccountTicketStatuses).toHaveBeenCalledWith(7)
    const text = wrapper.text()
    expect(text).toContain('admin.codex.ticket.state.ready')
    expect(text).toContain('admin.codex.ticket.state.missing')
    expect(text).toContain('gpt-6.1-sol')
    expect(text).toContain('gpt-6-astra')
  })

  it('does not request ticket statuses for ineligible accounts', async () => {
    const wrapper = mount(CodexTicketStatusCell, {
      props: { account: makeAccount({ type: 'apikey' }) }
    })
    await flushPromises()

    expect(getAccountTicketStatuses).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('-')
  })

  it('opens the ticket center when clicked', async () => {
    getAccountTicketStatuses.mockResolvedValue({
      account_id: 7,
      items: [makeTicketStatus()],
      manual_available: true,
      manual_unavailable_reason: ''
    })
    const account = makeAccount()
    const wrapper = mount(CodexTicketStatusCell, { props: { account } })
    await flushPromises()

    await wrapper.trigger('click')
    expect(wrapper.emitted('open')?.[0]).toEqual([account])
  })
})

describe('CodexDegradationModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listDegradationModels.mockResolvedValue({
      models: ['gpt-6.1-sol', 'gpt-6-astra'],
      bank: 'bank-commit-abc'
    })
    getDegradationSettings.mockResolvedValue({
      enabled: true,
      interval_minutes: 10,
      block_minutes: 30,
      models: ['gpt-6.1-sol']
    })
    listAccountDegradationResults.mockResolvedValue({
      items: [
        {
          account_id: 7,
          model: 'gpt-6.1-sol',
          status: 'normal',
          probability: 0.95,
          parsed_count: 355,
          http_status: 200,
          source: 'manual',
          created_at: '2026-01-01T00:00:00Z'
        }
      ]
    })
  })

  it('runs a diagnose request and renders the four-state result', async () => {
    diagnoseAccount.mockResolvedValue({
      account_id: 7,
      status: 'degraded',
      checked_at: '2026-01-01T00:00:00Z',
      models: ['gpt-6.1-sol'],
      items: [
        {
          model: 'gpt-6.1-sol',
          status: 'normal',
          predicted_model: 'gpt-6.1-sol',
          probability: 0.9,
          parsed_count: 355,
          http_status: 200
        },
        {
          model: 'gpt-6-astra',
          status: 'degraded',
          predicted_model: 'gpt-5.6-sol',
          probability: 0.876,
          parsed_count: 300,
          http_status: 200
        },
        {
          model: 'gpt-5.6-sol',
          status: 'uncertain',
          reason: 'reference_class'
        },
        {
          model: 'gpt-5.5',
          status: 'failed',
          reason: 'timeout'
        }
      ]
    })

    const wrapper = mount(CodexDegradationModal, {
      props: { show: true, account: makeAccount() },
      global: { stubs: { BaseDialog: BaseDialogStub } }
    })
    await flushPromises()

    const runButton = wrapper
      .findAll('button')
      .find(button => button.text().includes('admin.codex.degradation.run'))
    expect(runButton).toBeTruthy()
    await runButton!.trigger('click')
    await flushPromises()

    expect(diagnoseAccount).toHaveBeenCalledWith(7, ['gpt-6.1-sol'])
    const text = wrapper.text()
    expect(text).toContain('admin.codex.degradation.status.normal')
    expect(text).toContain('admin.codex.degradation.status.degraded')
    expect(text).toContain('admin.codex.degradation.status.uncertain')
    expect(text).toContain('admin.codex.degradation.status.failed')
    expect(text).toContain('gpt-5.6-sol')
    expect(text).toContain('87.6%')
    // 历史结果列表
    expect(listAccountDegradationResults).toHaveBeenCalledWith(7, 50)
  })
})
