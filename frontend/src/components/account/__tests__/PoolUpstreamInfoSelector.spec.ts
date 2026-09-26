import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PoolUpstreamInfoSelector from '../PoolUpstreamInfoSelector.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const mountSelector = (props: { platform?: string; features?: string[]; accountPlatform?: string } = {}) =>
  mount(PoolUpstreamInfoSelector, {
    props: {
      platform: 'default',
      features: [],
      accountPlatform: 'openai',
      ...props
    }
  })

describe('PoolUpstreamInfoSelector', () => {
  it('hides feature choices for the default platform', () => {
    const wrapper = mountSelector()
    expect(wrapper.find('[data-testid="pool-upstream-features"]').exists()).toBe(false)
  })

  it('shows only balance for sub2api', async () => {
    const wrapper = mountSelector()
    await wrapper.get('[data-testid="pool-upstream-platform"]').setValue('sub2api')
    expect(wrapper.emitted('update:platform')).toEqual([['sub2api']])

    await wrapper.setProps({ platform: 'sub2api', features: [] })
    expect(wrapper.find('[data-testid="pool-upstream-feature-balance"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="pool-upstream-feature-account_count"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pool-upstream-feature-image_quota"]').exists()).toBe(false)
  })

  it('shows both dashboard features for chatgpt2api on openai accounts', async () => {
    const wrapper = mountSelector({ platform: 'chatgpt2api', features: [], accountPlatform: 'openai' })
    expect(wrapper.find('[data-testid="pool-upstream-feature-account_count"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="pool-upstream-feature-image_quota"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="pool-upstream-feature-balance"]').exists()).toBe(false)
  })

  it('disables chatgpt2api for non-openai provider accounts', () => {
    const wrapper = mountSelector({ accountPlatform: 'anthropic' })
    const option = wrapper
      .get('[data-testid="pool-upstream-platform"]')
      .findAll('option')
      .find(candidate => candidate.attributes('value') === 'chatgpt2api')
    expect(option?.attributes('disabled')).toBeDefined()
  })

  it('clears a chatgpt2api selection when the provider leaves openai', async () => {
    const wrapper = mountSelector({
      platform: 'chatgpt2api',
      features: ['account_count', 'image_quota'],
      accountPlatform: 'openai'
    })
    await wrapper.setProps({ accountPlatform: 'anthropic' })
    expect(wrapper.emitted('update:platform')).toEqual([['default']])
    expect(wrapper.emitted('update:features')).toEqual([[[]]])
  })

  it('keeps the selection when the provider stays openai', async () => {
    const wrapper = mountSelector({
      platform: 'chatgpt2api',
      features: ['account_count'],
      accountPlatform: 'openai'
    })
    await wrapper.setProps({ accountPlatform: 'openai' })
    expect(wrapper.emitted('update:platform')).toBeUndefined()
  })

  it('drops incompatible features when the platform changes', async () => {
    const wrapper = mountSelector({ platform: 'chatgpt2api', features: ['account_count', 'image_quota'] })
    await wrapper.get('[data-testid="pool-upstream-platform"]').setValue('sub2api')
    expect(wrapper.emitted('update:platform')).toEqual([['sub2api']])
    expect(wrapper.emitted('update:features')).toEqual([[[]]])
  })

  it('keeps compatible features when switching between platforms that share them', async () => {
    const wrapper = mountSelector({ platform: 'sub2api', features: ['balance'] })
    await wrapper.get('[data-testid="pool-upstream-platform"]').setValue('chatgpt2api')
    expect(wrapper.emitted('update:features')).toEqual([[[]]])
  })

  it('emits feature toggles', async () => {
    const wrapper = mountSelector({ platform: 'chatgpt2api', features: ['account_count'] })
    await wrapper.get('[data-testid="pool-upstream-feature-image_quota"]').setValue(true)
    expect(wrapper.emitted('update:features')).toEqual([[['account_count', 'image_quota']]])

    await wrapper.setProps({ features: ['account_count', 'image_quota'] })
    await wrapper.get('[data-testid="pool-upstream-feature-account_count"]').setValue(false)
    expect(wrapper.emitted('update:features')?.[1]).toEqual([['image_quota']])
  })

  it('normalizes unknown platform values back to default', async () => {
    const wrapper = mountSelector({ platform: 'sub2api' })
    const select = wrapper.get<HTMLSelectElement>('[data-testid="pool-upstream-platform"]')
    select.element.value = 'bogus'
    await select.trigger('change')
    expect(wrapper.emitted('update:platform')).toEqual([['default']])
  })
})
