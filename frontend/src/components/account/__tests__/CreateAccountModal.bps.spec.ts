import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { createAccountMock, showErrorMock } = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  showErrorMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: vi.fn(),
    showWarning: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      probeUpstreamBilling: vi.fn().mockResolvedValue({}),
      syncUpstreamModels: vi.fn().mockResolvedValue({ models: [], metadata: {} }),
      importCodexSession: vi.fn(),
      createOpenAICodexPAT: vi.fn()
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

import CreateAccountModal from '../CreateAccountModal.vue'

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

function mountModal() {
  return mount(CreateAccountModal, {
    props: { show: true, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        ConfirmDialog: ConfirmDialogStub,
        OAuthAuthorizationFlow: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        ProxySelector: true,
        ProxyAdBanner: true,
        GroupSelector: true,
        ModelWhitelistSelector: true,
        QuotaLimitCard: true
      }
    }
  })
}

async function selectBPSPlatform(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('[data-testid="platform-button-openai_bps"]').trigger('click')
}

async function fillBPSForm(wrapper: ReturnType<typeof mountModal>, token = 'bps-token') {
  await wrapper.get('form#create-account-form input[type="text"]').setValue('bps account')
  await wrapper.get('[data-testid="create-bps-access-token"]').setValue(token)
}

describe('CreateAccountModal OpenAI BPS', () => {
  beforeEach(() => {
    createAccountMock.mockReset().mockResolvedValue({ id: 9, platform: 'openai_bps', type: 'oauth' })
    showErrorMock.mockReset()
  })

  it('shows BPS fields and hides the API Key form when the BPS platform is selected', async () => {
    const wrapper = mountModal()
    await selectBPSPlatform(wrapper)

    expect(wrapper.find('[data-testid="create-bps-fields"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="create-bps-access-token"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="create-bps-account-id"]').exists()).toBe(true)
    // BPS 只支持 Access Token：不出现通用 API Key 输入。
    expect(wrapper.find('form#create-account-form input[placeholder*="apiKey"]').exists()).toBe(false)
    expect(wrapper.findAll('form#create-account-form input[type="password"]').length).toBe(1)
  })

  it('rejects an empty access token without submitting', async () => {
    const wrapper = mountModal()
    await selectBPSPlatform(wrapper)
    await wrapper.get('form#create-account-form input[type="text"]').setValue('bps account')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.bps.tokenRequired')
  })

  it('asks for ban-risk confirmation before creating and submits on confirm', async () => {
    const wrapper = mountModal()
    await selectBPSPlatform(wrapper)
    await fillBPSForm(wrapper, ' Bearer bps-token ')
    await wrapper.get('[data-testid="create-bps-account-id"]').setValue('acct-1')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    // 风险确认前不创建账号
    expect(createAccountMock).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(true)

    await wrapper.get('[data-testid="dialog-confirm"]').trigger('click')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0][0]
    expect(payload.platform).toBe('openai_bps')
    expect(payload.type).toBe('oauth')
    expect(payload.credentials).toEqual({ access_token: 'bps-token', chatgpt_account_id: 'acct-1' })
  })

  it('keeps the entered token when the risk confirmation is cancelled', async () => {
    const wrapper = mountModal()
    await selectBPSPlatform(wrapper)
    await fillBPSForm(wrapper, 'bps-token')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    await wrapper.get('[data-testid="dialog-cancel"]').trigger('click')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(false)
    expect((wrapper.get('[data-testid="create-bps-access-token"]').element as HTMLInputElement).value).toBe('bps-token')
  })
})
