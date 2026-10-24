import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Account } from '@/types'

const { testOpenAIBPSConnectionMock } = vi.hoisted(() => ({
  testOpenAIBPSConnectionMock: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      testOpenAIBPSConnection: testOpenAIBPSConnectionMock
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
    })
  }
})

import OpenAIBPSConnectionTestModal from '../OpenAIBPSConnectionTestModal.vue'

const account: Account = {
  id: 7,
  name: 'bps-account',
  platform: 'openai_bps',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  concurrency: 1,
  priority: 1,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  auto_pause_on_expired: false,
  error_message: null,
  last_used_at: null,
  expires_at: null,
  proxy_id: null
} as Account

function mountModal() {
  return mount(OpenAIBPSConnectionTestModal, {
    props: { show: true, account },
    global: {
      stubs: {
        BaseDialog: {
          props: { show: Boolean },
          template: '<div v-if="show"><slot /><slot name="footer" /></div>'
        },
        PlatformIcon: true
      }
    }
  })
}

describe('OpenAIBPSConnectionTestModal', () => {
  beforeEach(() => {
    testOpenAIBPSConnectionMock.mockReset()
  })

  it('calls the BPS test endpoint and renders a successful result', async () => {
    testOpenAIBPSConnectionMock.mockResolvedValue({
      account_id: 7,
      model: 'gpt-6-astra',
      compact: false,
      http_status: 200,
      upstream_model: 'gpt-6-astra',
      request_id: 'req-1',
      error_code: '',
      duration_ms: 123
    })
    const wrapper = mountModal()
    await wrapper.get('[data-testid="bps-test-run"]').trigger('click')
    await flushPromises()

    expect(testOpenAIBPSConnectionMock).toHaveBeenCalledWith(7, { model: '', compact: false })
    const result = wrapper.get('[data-testid="bps-test-result"]')
    expect(result.text()).toContain('admin.accounts.bps.testConnection.success')
    expect(result.get('[data-testid="bps-test-http-status"]').text()).toBe('200')
    expect(result.get('[data-testid="bps-test-upstream-model"]').text()).toBe('gpt-6-astra')
    expect(result.get('[data-testid="bps-test-request-id"]').text()).toBe('req-1')
  })

  it('sends the model override and compact flag', async () => {
    testOpenAIBPSConnectionMock.mockResolvedValue({
      account_id: 7,
      model: 'gpt-5.6-sol',
      compact: true,
      http_status: 200,
      upstream_model: '',
      request_id: '',
      error_code: '',
      duration_ms: 1
    })
    const wrapper = mountModal()
    await wrapper.get('[data-testid="bps-test-model"]').setValue(' gpt-5.6-sol ')
    await wrapper.get('[role="switch"]').trigger('click')
    await wrapper.get('[data-testid="bps-test-run"]').trigger('click')
    await flushPromises()

    expect(testOpenAIBPSConnectionMock).toHaveBeenCalledWith(7, { model: 'gpt-5.6-sol', compact: true })
  })

  it('shows the upstream error code for a failed probe', async () => {
    testOpenAIBPSConnectionMock.mockResolvedValue({
      account_id: 7,
      model: 'gpt-6-astra',
      compact: false,
      http_status: 401,
      upstream_model: '',
      request_id: '',
      error_code: 'token_revoked',
      duration_ms: 9
    })
    const wrapper = mountModal()
    await wrapper.get('[data-testid="bps-test-run"]').trigger('click')
    await flushPromises()

    const result = wrapper.get('[data-testid="bps-test-result"]')
    expect(result.text()).toContain('admin.accounts.bps.testConnection.failed')
    expect(result.get('[data-testid="bps-test-error-code"]').text()).toBe('token_revoked')
  })

  it('shows a local error when the request itself fails', async () => {
    testOpenAIBPSConnectionMock.mockRejectedValue({ message: 'account not found' })
    const wrapper = mountModal()
    await wrapper.get('[data-testid="bps-test-run"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="bps-test-error"]').text()).toContain('account not found')
    expect(wrapper.find('[data-testid="bps-test-result"]').exists()).toBe(false)
  })
})
