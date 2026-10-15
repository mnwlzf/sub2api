import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { PelicanProbePlan, PelicanProbePublicStatus, PelicanProbeResult } from '@/types'
import IQStatusView from '@/views/user/IQStatusView.vue'

// 用户端「智商状态」页：状态卡片式布局 + 分组聚合 + 失败态说人话。
// 关键红线：用户端绝不能渲染原始 error_message（含内网 URL / 传输细节）。

const { getPublicStatus } = vi.hoisted(() => ({ getPublicStatus: vi.fn() }))

vi.mock('@/api/pelicanProbe', () => ({
  getPublicStatus,
  pelicanProbeUserAPI: { getPublicStatus },
  default: { getPublicStatus }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() })
}))

// t() 直接返回 key，便于断言「渲染了哪个 i18n key」。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  Select: { props: ['modelValue', 'options'], template: '<div class="select-stub" />' }
}

const INTERNAL_ERROR = 'Post "http://127.0.0.1:8080/v1/chat/completions": context canceled'

function makePlan(overrides: Partial<PelicanProbePlan> = {}): PelicanProbePlan {
  return {
    id: 1,
    name: 'plan-1',
    group_id: 10,
    model_id: 'gpt-5',
    base_url: '',
    has_api_key: true,
    test_type: 'pelican',
    reasoning_effort: '',
    judge_model_id: '',
    cron_expression: '0 * * * *',
    enabled: true,
    is_public: true,
    group_name: 'Alpha',
    max_results: 50,
    last_run_at: null,
    next_run_at: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides
  }
}

function makeResult(overrides: Partial<PelicanProbeResult> = {}): PelicanProbeResult {
  return {
    id: 100,
    plan_id: 1,
    status: 'success',
    raw_html: '<html><body>pelican</body></html>',
    verdict: 'pass',
    note: '',
    reviewed_by: '',
    reviewed_at: null,
    error_message: '',
    latency_ms: 1200,
    prompt_tokens: 10,
    completion_tokens: 20,
    started_at: '2026-01-01T00:00:00Z',
    finished_at: '2026-01-01T00:00:01Z',
    created_at: '2026-01-01T00:00:01Z',
    ...overrides
  }
}

async function mountView(status: PelicanProbePublicStatus) {
  getPublicStatus.mockResolvedValue(status)
  const wrapper = mount(IQStatusView, { global: { stubs } })
  await flushPromises()
  return wrapper
}

const enabledStatus = (
  plans: PelicanProbePlan[],
  results: PelicanProbeResult[]
): PelicanProbePublicStatus => ({ enabled: true, plans, results })

/** 页面顶部还有「刷新」按钮，所以历史开关必须按文案定位。 */
async function clickHistoryToggle(wrapper: ReturnType<typeof mount>) {
  const toggle = wrapper
    .findAll('button')
    .find((b) => b.text().includes('admin.pelicanProbe.viewHistory'))
  expect(toggle, 'history toggle button should exist').toBeTruthy()
  await toggle!.trigger('click')
}

describe('IQStatusView (user-facing IQ status)', () => {
  beforeEach(() => {
    getPublicStatus.mockReset()
  })

  it('constrains the page width so the layout is not stretched full-bleed', async () => {
    const wrapper = await mountView(enabledStatus([makePlan()], [makeResult()]))
    expect(wrapper.find('.max-w-5xl').exists()).toBe(true)
  })

  it('groups plans by group_id into one section per group', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [
          makePlan({ id: 1, group_id: 10, group_name: 'Alpha', model_id: 'gpt-5' }),
          makePlan({ id: 2, group_id: 10, group_name: 'Alpha', model_id: 'gpt-5-mini' }),
          makePlan({ id: 3, group_id: 20, group_name: 'Beta', model_id: 'claude-4' })
        ],
        [makeResult({ id: 100, plan_id: 1 })]
      )
    )

    const sections = wrapper.findAll('section')
    expect(sections).toHaveLength(2)
    expect(sections[0].find('h2').text()).toBe('Alpha')
    expect(sections[1].find('h2').text()).toBe('Beta')
    // 两个 plan 同组 => 该分组下两张卡片
    expect(sections[0].findAll('article')).toHaveLength(2)
    expect(sections[1].findAll('article')).toHaveLength(1)
  })

  it('maps the latest result to one of the four status states', async () => {
    const cases: Array<[Partial<PelicanProbeResult>, string]> = [
      [{ status: 'success', verdict: 'pass' }, 'admin.pelicanProbe.statusNormal'],
      [{ status: 'success', verdict: 'degraded' }, 'admin.pelicanProbe.statusDegraded'],
      [{ status: 'success', verdict: 'pending' }, 'admin.pelicanProbe.statusPending'],
      [{ status: 'failed', verdict: 'pending' }, 'admin.pelicanProbe.statusNoData']
    ]

    for (const [overrides, expectedKey] of cases) {
      const wrapper = await mountView(
        enabledStatus([makePlan()], [makeResult(overrides)])
      )
      const badge = wrapper.find('article')
      expect(badge.text()).toContain(expectedKey)
      wrapper.unmount()
    }
  })

  it('shows "no valid result" when the plan has no result at all', async () => {
    const wrapper = await mountView(enabledStatus([makePlan()], []))
    const card = wrapper.find('article')
    expect(card.text()).toContain('admin.pelicanProbe.statusNoData')
    expect(card.text()).toContain('admin.pelicanProbe.noResults')
    expect(wrapper.find('iframe').exists()).toBe(false)
  })

  it('translates failure_reason into plain language', async () => {
    const cases: Array<[PelicanProbeResult['failure_reason'], string]> = [
      ['upstream', 'admin.pelicanProbe.failureUpstream'],
      ['timeout', 'admin.pelicanProbe.failureTimeout'],
      ['concurrency', 'admin.pelicanProbe.failureConcurrency'],
      ['config', 'admin.pelicanProbe.failureConfig'],
      ['unknown', 'admin.pelicanProbe.failureUnknown']
    ]

    for (const [reason, expectedKey] of cases) {
      const wrapper = await mountView(
        enabledStatus(
          [makePlan()],
          [makeResult({ status: 'failed', verdict: 'pending', failure_reason: reason })]
        )
      )
      expect(wrapper.find('article').text()).toContain(expectedKey)
      wrapper.unmount()
    }
  })

  it('falls back to the generic message when the backend omits failure_reason', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan()],
        [makeResult({ status: 'failed', verdict: 'pending', error_message: INTERNAL_ERROR })]
      )
    )
    expect(wrapper.find('article').text()).toContain('admin.pelicanProbe.failureUnknown')
  })

  it('never renders the raw error_message to end users', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan()],
        [
          makeResult({
            id: 101,
            status: 'failed',
            verdict: 'pending',
            failure_reason: 'upstream',
            error_message: INTERNAL_ERROR
          }),
          makeResult({
            id: 100,
            status: 'failed',
            verdict: 'pending',
            error_message: INTERNAL_ERROR
          })
        ]
      )
    )

    // 展开历史，确保历史卡片也不会泄露
    await clickHistoryToggle(wrapper)
    await flushPromises()

    const html = wrapper.html()
    expect(html).not.toContain('127.0.0.1')
    expect(html).not.toContain('context canceled')
    expect(html).not.toContain('chat/completions')
    expect(html).not.toContain(INTERNAL_ERROR)
  })

  it('renders only the latest result as a card and keeps older ones behind history', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan()],
        [
          makeResult({ id: 103, created_at: '2026-01-03T00:00:00Z' }),
          makeResult({ id: 102, created_at: '2026-01-02T00:00:00Z' }),
          makeResult({ id: 101, created_at: '2026-01-01T00:00:00Z' })
        ]
      )
    )

    expect(wrapper.findAll('iframe[title="pelican probe preview"]')).toHaveLength(1)
    expect(wrapper.findAll('iframe[title="pelican probe history preview"]')).toHaveLength(0)
    expect(wrapper.text()).toContain('admin.pelicanProbe.viewHistory')

    await clickHistoryToggle(wrapper)
    await flushPromises()

    expect(wrapper.findAll('iframe[title="pelican probe history preview"]')).toHaveLength(2)
  })

  it('shows a compact preview thumbnail rather than a tall empty frame', async () => {
    const wrapper = await mountView(enabledStatus([makePlan()], [makeResult()]))
    const frame = wrapper.find('iframe[title="pelican probe preview"]')
    expect(frame.exists()).toBe(true)
    expect(frame.attributes('sandbox')).toBe('allow-scripts')
    expect(frame.attributes('class')).toContain('h-[180px]')
  })
})
