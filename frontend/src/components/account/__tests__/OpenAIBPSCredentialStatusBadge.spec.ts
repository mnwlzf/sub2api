import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
  }
})

import OpenAIBPSCredentialStatusBadge from '../OpenAIBPSCredentialStatusBadge.vue'

describe('OpenAIBPSCredentialStatusBadge', () => {
  it('renders nothing for non-BPS platforms', () => {
    const wrapper = mount(OpenAIBPSCredentialStatusBadge, {
      props: { platform: 'openai', state: { status: 'revoked' } }
    })
    expect(wrapper.find('[data-testid="bps-credential-status-badge"]').exists()).toBe(false)
  })

  it('renders nothing when the backend omits bps_credential_state', () => {
    const wrapper = mount(OpenAIBPSCredentialStatusBadge, {
      props: { platform: 'openai_bps', state: null }
    })
    expect(wrapper.find('[data-testid="bps-credential-status-badge"]').exists()).toBe(false)
  })

  it('renders the normal status badge', () => {
    const wrapper = mount(OpenAIBPSCredentialStatusBadge, {
      props: { platform: 'openai_bps', state: { status: 'normal' } }
    })
    const badge = wrapper.get('[data-testid="bps-credential-status-badge"]')
    expect(badge.attributes('data-status')).toBe('normal')
    expect(badge.text()).toContain('admin.accounts.bps.credentialStatus.normal')
  })

  it('renders abnormal statuses with tooltip details', () => {
    const wrapper = mount(OpenAIBPSCredentialStatusBadge, {
      props: {
        platform: 'openai_bps',
        state: {
          status: 'revoked',
          expires_at: '2026-01-01T00:00:00Z',
          last_seen_at: '2026-01-02T00:00:00Z',
          error_code: 'token_revoked'
        }
      }
    })
    const badge = wrapper.get('[data-testid="bps-credential-status-badge"]')
    expect(badge.attributes('data-status')).toBe('revoked')
    expect(badge.text()).toContain('admin.accounts.bps.credentialStatus.revoked')
    const title = badge.attributes('title') ?? ''
    expect(title).toContain('token_revoked')
    expect(title).toContain('admin.accounts.bps.credentialStatus.expiresAt')
    expect(title).toContain('admin.accounts.bps.credentialStatus.lastSeenAt')
    expect(title).toContain('admin.accounts.bps.credentialStatus.errorCode')
  })
})
