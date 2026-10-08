import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

const tCalls = vi.hoisted(() => [] as { key: string; params?: Record<string, unknown> }[])

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: { value: 'en' },
      t: (key: string, params?: Record<string, unknown>) => {
        tCalls.push({ key, params })
        return key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? `{${token}}`))
      },
    }),
  }
})

import TemplateEventPanel from '../components/TemplateEventPanel.vue'
import type { PromptAdminEvent } from '../types'

const SHA = 'a1b2c3d4e5f60718293a4b5c6d7e8f90112233445566778899aabbccddeeff00'

function event(overrides: Partial<PromptAdminEvent> = {}): PromptAdminEvent {
  return {
    id: 5,
    action: 'update_template',
    scope: 'template',
    template_id: 1,
    actor_id: 9,
    actor_name: 'ops-admin',
    before_state: { name: 'before-name' },
    after_state: { name: 'after-name' },
    created_at: '2026-10-08T06:00:00Z',
    ...overrides,
  }
}

function mountPanel(events: PromptAdminEvent[], error = '') {
  return mount(TemplateEventPanel, {
    props: { events, loading: false, error },
  })
}

describe('TemplateEventPanel', () => {
  it('renders the fixed action/scope enums through i18n with a raw fallback', () => {
    const wrapper = mountPanel([event()])

    expect(wrapper.text()).toContain('admin.promptTemplates.audit.actions.update_template')
    expect(wrapper.text()).toContain('admin.promptTemplates.audit.scopes.template')
    expect(wrapper.text()).toContain('ops-admin')

    const unknown = mountPanel([event({ id: 6, action: 'brand_new_action', scope: 'brand_new_scope' })])
    expect(unknown.text()).toContain('admin.promptTemplates.audit.actions.unknown')
    expect(unknown.text()).toContain('admin.promptTemplates.audit.scopes.unknown')
    expect(unknown.text()).toContain('brand_new_action')
  })

  it('falls back to the actor id and then to an explicit "not recorded"', () => {
    tCalls.length = 0
    const byId = mountPanel([event({ id: 7, actor_name: undefined })])
    expect(byId.text()).toContain('admin.promptTemplates.audit.actorId')
    expect(tCalls).toContainEqual({
      key: 'admin.promptTemplates.audit.actorId',
      params: { id: 9 },
    })

    const anonymous = mountPanel([event({ id: 8, actor_id: undefined, actor_name: undefined })])
    expect(anonymous.text()).toContain('admin.promptTemplates.audit.actorUnknown')
  })

  it('summarises before/after as key-value chips instead of dumping raw JSON', () => {
    const wrapper = mountPanel([event()])

    expect(wrapper.text()).toContain('admin.promptTemplates.audit.stateKeys.name')
    expect(wrapper.text()).toContain('before-name')
    expect(wrapper.text()).toContain('after-name')
    expect(wrapper.find('pre').exists()).toBe(false)
  })

  it('shortens hashes in the summary and reveals the raw state only on demand', async () => {
    const wrapper = mountPanel([
      event({ id: 12, action: 'publish_version', scope: 'version', before_state: undefined, after_state: { body_sha256: SHA } }),
    ])

    expect(wrapper.text()).toContain('a1b2c3d4e5f6…')
    expect(wrapper.text()).not.toContain(SHA)

    await wrapper.find('[data-test="audit-raw-12"]').trigger('click')

    const raw = wrapper.find('pre').text()
    expect(raw).toContain(SHA)
    expect(raw).toContain('after_state')
    expect(wrapper.text()).toContain('admin.promptTemplates.audit.hideRaw')
  })

  it('translates the fixed mode enum in state chips', () => {
    const wrapper = mountPanel([
      event({ id: 13, action: 'set_binding', scope: 'binding', before_state: undefined, after_state: { mode: 'disabled' } }),
    ])

    expect(wrapper.text()).toContain('admin.promptTemplates.audit.stateKeys.mode')
    expect(wrapper.text()).toContain('admin.promptTemplates.binding.disabled')
  })

  it('never claims "no records" while the audit load has failed', () => {
    const wrapper = mountPanel([], 'audit unavailable')

    expect(wrapper.find('[role="alert"]').text()).toBe('audit unavailable')
    expect(wrapper.text()).not.toContain('admin.promptTemplates.audit.empty')
  })
})
