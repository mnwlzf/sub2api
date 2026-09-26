import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import PoolUpstreamInfoCell from '../PoolUpstreamInfoCell.vue'
import type { Account } from '@/types'

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

const makeAccount = (overrides: Partial<Account> = {}): Account => ({
  id: 1,
  name: 'pool-upstream',
  platform: 'openai',
  type: 'apikey',
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-09-27T00:00:00Z',
  updated_at: '2026-09-27T00:00:00Z',
  schedulable: true,
  credentials: { pool_mode: true },
  rate_limited_at: null,
  rate_limit_reset_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null,
  ...overrides
})

const configuredExtra = (snapshot?: Record<string, unknown>) => ({
  pool_upstream_platform: 'sub2api',
  pool_upstream_features: ['balance'],
  ...(snapshot ? { pool_upstream_info: snapshot } : {})
})

const okSnapshot = (overrides: Record<string, unknown> = {}) => ({
  status: 'ok',
  platform: 'sub2api',
  features: ['balance'],
  data: { kind: 'wallet', amount_usd: 12.5 },
  received_at: '2026-09-27T00:00:00Z',
  fresh_until: '2026-09-27T01:00:00Z',
  last_attempt_at: '2026-09-27T00:00:00Z',
  next_probe_at: '2026-09-27T00:30:00Z',
  http_status: 200,
  ...overrides
})

describe('PoolUpstreamInfoCell', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-27T00:10:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders a dash for accounts without an opt-in selection', () => {
    const wrapper = mount(PoolUpstreamInfoCell, {
      props: { account: makeAccount(), now: Date.now() }
    })
    expect(wrapper.get('[data-testid="pool-upstream-info-empty"]').text()).toBe('-')
    expect(wrapper.find('[data-testid="pool-upstream-info-probe"]').exists()).toBe(false)
  })

  it('renders a dash for selections on non-apikey accounts', () => {
    const wrapper = mount(PoolUpstreamInfoCell, {
      props: { account: makeAccount({ type: 'oauth', extra: configuredExtra() }), now: Date.now() }
    })
    expect(wrapper.get('[data-testid="pool-upstream-info-empty"]').text()).toBe('-')
  })

  it('shows not-probed state and emits a manual probe', async () => {
    const wrapper = mount(PoolUpstreamInfoCell, {
      props: { account: makeAccount({ extra: configuredExtra() }), now: Date.now() }
    })
    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.notProbed'
    )
    await wrapper.get('[data-testid="pool-upstream-info-probe"]').trigger('click')
    expect(wrapper.emitted('probe')).toHaveLength(1)
  })

  it('renders a dash when pool mode is off even with a stored selection', () => {
    const wrapper = mount(PoolUpstreamInfoCell, {
      props: {
        account: makeAccount({
          credentials: { pool_mode: false },
          extra: configuredExtra(okSnapshot())
        }),
        now: Date.now()
      }
    })
    expect(wrapper.get('[data-testid="pool-upstream-info-empty"]').text()).toBe('-')
    expect(wrapper.find('[data-testid="pool-upstream-info-probe"]').exists()).toBe(false)
  })

  it('renders wallet balance as a labeled primary value', () => {
    const wrapper = mount(PoolUpstreamInfoCell, {
      props: { account: makeAccount({ extra: configuredExtra(okSnapshot()) }), now: Date.now() }
    })
    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.amount_wallet:$12.50'
    )
  })

  it('renders chatgpt2api counts as labeled compact text, not money', () => {
    const account = makeAccount({
      extra: {
        pool_upstream_platform: 'chatgpt2api',
        pool_upstream_features: ['account_count', 'image_quota'],
        pool_upstream_info: okSnapshot({
          platform: 'chatgpt2api',
          features: ['account_count', 'image_quota'],
          data: { accounts_active: 1, total_quota: 25 }
        })
      }
    })
    const wrapper = mount(PoolUpstreamInfoCell, { props: { account, now: Date.now() } })
    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.accountsActive:1 · admin.accounts.poolUpstream.totalQuota:25'
    )
    expect(wrapper.text()).not.toContain('$')
  })

  it('marks stale snapshots while keeping the last value visible', () => {
    const account = makeAccount({
      extra: configuredExtra(
        okSnapshot({
          fresh_until: '2026-09-27T00:05:00Z' // already past `now`
        })
      )
    })
    const wrapper = mount(PoolUpstreamInfoCell, { props: { account, now: Date.now() } })
    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.amount_wallet:$12.50'
    )
    expect(wrapper.text()).toContain('admin.accounts.poolUpstream.stale')
  })

  it('shows failed state when the last attempt failed and data is absent', async () => {
    const account = makeAccount({
      extra: configuredExtra({
        status: 'failed',
        platform: 'sub2api',
        features: ['balance'],
        last_attempt_at: '2026-09-27T00:00:00Z',
        next_probe_at: '2026-09-27T00:30:00Z',
        last_error: 'unauthorized'
      })
    })
    const wrapper = mount(PoolUpstreamInfoCell, {
      attachTo: document.body,
      props: { account, now: Date.now() }
    })
    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.failed'
    )
    // The error detail lives in the tooltip, which teleports to document.body.
    await wrapper.get('[data-testid="pool-upstream-info-details"]').trigger('mouseenter')
    await flushPromises()
    const error = document.body.querySelector('[data-testid="pool-upstream-info-error"]')
    // The mocked t() returns the key unchanged, so the cell falls back to the
    // raw machine-safe reason.
    expect(error?.textContent).toBe('unauthorized')
    wrapper.unmount()
  })

  it('preserves last value on failed attempts while showing the failure', () => {
    const account = makeAccount({
      extra: configuredExtra(
        okSnapshot({
          status: 'failed',
          last_error: 'http_error',
          http_status: 500
        })
      )
    })
    const wrapper = mount(PoolUpstreamInfoCell, { props: { account, now: Date.now() } })
    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.amount_wallet:$12.50'
    )
    expect(wrapper.text()).toContain('admin.accounts.poolUpstream.failed')
  })

  it('renders unsupported without data lines', () => {
    const account = makeAccount({
      extra: configuredExtra({
        status: 'unsupported',
        platform: 'sub2api',
        features: ['balance'],
        data: { kind: 'wallet', amount_usd: 99 },
        last_attempt_at: '2026-09-27T00:00:00Z',
        next_probe_at: '2026-09-27T06:00:00Z',
        last_error: 'unsupported'
      })
    })
    const wrapper = mount(PoolUpstreamInfoCell, { props: { account, now: Date.now() } })
    expect(wrapper.get('[data-testid="pool-upstream-info"]').text()).toBe(
      'admin.accounts.poolUpstream.unsupported'
    )
    expect(wrapper.text()).not.toContain('$99.00')
  })

  it('disables the probe button while probing', () => {
    const wrapper = mount(PoolUpstreamInfoCell, {
      props: { account: makeAccount({ extra: configuredExtra(okSnapshot()) }), now: Date.now(), probing: true }
    })
    expect(wrapper.get('[data-testid="pool-upstream-info-probe"]').attributes('disabled')).toBeDefined()
  })
})
