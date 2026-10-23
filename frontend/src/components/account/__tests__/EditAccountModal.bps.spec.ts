import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import type { Account } from '@/types'

const { updateAccountMock, checkMixedChannelRiskMock, showErrorMock } = vi.hoisted(() => ({
  updateAccountMock: vi.fn(),
  checkMixedChannelRiskMock: vi.fn(),
  showErrorMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      update: updateAccountMock,
      checkMixedChannelRisk: checkMixedChannelRiskMock
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([])
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([])
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import EditAccountModal from '../EditAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const ConfirmDialogStub = defineComponent({
  name: 'ConfirmDialog',
  props: {
    show: { type: Boolean, default: false },
    title: String,
    message: String,
    confirmText: String,
    cancelText: String
  },
  emits: ['confirm', 'cancel'],
  template: `
    <div v-if="show" data-testid="confirm-dialog">
      <button data-testid="dialog-confirm" @click="$emit('confirm')">confirm</button>
      <button data-testid="dialog-cancel" @click="$emit('cancel')">cancel</button>
    </div>
  `
})

function buildBPSAccount(overrides: Partial<Account> = {}): Account {
  return {
    id: 11,
    name: 'BPS account',
    notes: '',
    platform: 'openai_bps',
    type: 'oauth',
    credentials: { chatgpt_account_id: 'acct-1' },
    credentials_status: { has_access_token: true },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    schedulable: true,
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false,
    error_message: null,
    last_used_at: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides
  } as Account
}

function mountModal(account = buildBPSAccount()) {
  return mount(EditAccountModal, {
    props: { show: true, account, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        ConfirmDialog: ConfirmDialogStub,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        ProxySelector: true,
        GroupSelector: true,
        ModelWhitelistSelector: true
      }
    }
  })
}

describe('EditAccountModal OpenAI BPS', () => {
  beforeEach(() => {
    updateAccountMock.mockReset().mockResolvedValue(buildBPSAccount())
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    showErrorMock.mockReset()
  })

  it('shows BPS credential fields only for BPS accounts', () => {
    const bps = mountModal()
    expect(bps.find('[data-testid="edit-bps-fields"]').exists()).toBe(true)
    expect(
      (bps.get('[data-testid="edit-bps-account-id"]').element as HTMLInputElement).value
    ).toBe('acct-1')

    const openai = mountModal(buildBPSAccount({ platform: 'openai' }))
    expect(openai.find('[data-testid="edit-bps-fields"]').exists()).toBe(false)
  })

  it('keeps the stored token when the token field is left blank', async () => {
    const wrapper = mountModal()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(updateAccountMock).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(true)

    await wrapper.get('[data-testid="dialog-confirm"]').trigger('click')
    await flushPromises()

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    const payload = updateAccountMock.mock.calls[0][1]
    expect(payload.credentials.access_token).toBeUndefined()
    expect(payload.credentials.chatgpt_account_id).toBe('acct-1')
  })

  it('submits a rotated access token after confirmation', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="edit-bps-access-token"]').setValue(' Bearer new-token ')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()

    await wrapper.get('[data-testid="dialog-confirm"]').trigger('click')
    await flushPromises()

    const payload = updateAccountMock.mock.calls[0][1]
    expect(payload.credentials.access_token).toBe('new-token')
  })

  it('clears chatgpt_account_id when the field is emptied', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="edit-bps-account-id"]').setValue('')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()

    await wrapper.get('[data-testid="dialog-confirm"]').trigger('click')
    await flushPromises()

    const payload = updateAccountMock.mock.calls[0][1]
    expect(payload.credentials.chatgpt_account_id).toBeUndefined()
  })

  it('does not submit when the ban-risk confirmation is cancelled', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="edit-bps-access-token"]').setValue('new-token')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()

    await wrapper.get('[data-testid="dialog-cancel"]').trigger('click')
    await flushPromises()

    expect(updateAccountMock).not.toHaveBeenCalled()
    expect(
      (wrapper.get('[data-testid="edit-bps-access-token"]').element as HTMLInputElement).value
    ).toBe('new-token')
  })
})
