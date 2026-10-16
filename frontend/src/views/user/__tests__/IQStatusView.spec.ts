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

  it('gives the preview the full card width and a tall frame', async () => {
    const wrapper = await mountView(enabledStatus([makePlan()], [makeResult()]))
    const frame = wrapper.find('iframe[title="pelican probe preview"]')
    expect(frame.exists()).toBe(true)
    expect(frame.attributes('sandbox')).toBe('allow-scripts')
    // 用户明确要求预览占满卡片宽度、明显更高：不再是 sm:w-64 的固定窄列。
    expect(frame.attributes('class')).toContain('w-full')
    expect(frame.attributes('class')).toContain('h-[320px]')
    expect(wrapper.html()).not.toContain('sm:w-64')
  })

  // 用户第三次否定布局：不要卡片套卡片，状态改成标题行最前面的内联小胶囊，
  // 信息前置，预览单独占一行。
  it('lays the card out flat: inline pill leading the header, no nested panel', async () => {
    const wrapper = await mountView(
      enabledStatus([makePlan({ group_name: 'Alpha', model_id: 'gpt-6.1-sol' })], [makeResult()])
    )

    const card = wrapper.find('article')
    const html = card.html()

    // 彩色的、撑满卡片高度的内层状态面板必须彻底消失
    expect(html).not.toContain('self-stretch')
    expect(html).not.toContain('sm:w-28')
    expect(html).not.toContain('rounded-lg px-3 py-2')

    // 状态是标题行最前面的内联胶囊：rounded-full + 软色底，高度随内容
    const pill = card.find('span.rounded-full')
    expect(pill.exists()).toBe(true)
    expect(pill.classes()).toContain('inline-flex')
    expect(pill.classes()).not.toContain('self-stretch')
    expect(pill.text()).toContain('admin.pelicanProbe.statusNormal')

    // 卡片头部只有两行：胶囊 + 模型 / 时间·耗时 + 历史按钮
    const header = card.element.firstElementChild as HTMLElement
    expect(header.children).toHaveLength(2)
    expect(header.textContent).toContain('gpt-6.1-sol')

    // 分组名已在 section 标题里，卡片里不再重复
    expect(card.text()).not.toContain('Alpha')
    expect(card.find('.font-mono').text()).toBe('gpt-6.1-sol')

    // 预览是独立的一整行（头部 + 预览行），而不是挤在右侧的窄列
    expect(card.element.children).toHaveLength(2)
  })

  it('right-aligns the history toggle on the meta row inside the card', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan()],
        [
          makeResult({ id: 103, created_at: '2026-01-03T00:00:00Z' }),
          makeResult({ id: 102, created_at: '2026-01-02T00:00:00Z' })
        ]
      )
    )

    const card = wrapper.find('article')
    const toggle = card
      .findAll('button')
      .find((b) => b.text().includes('admin.pelicanProbe.viewHistory'))
    expect(toggle, 'history toggle lives inside the card header').toBeTruthy()
    // 与「时间 · 耗时」同属第二行，由 justify-between 推到右侧
    expect(toggle!.element.parentElement?.className).toContain('justify-between')
    expect(toggle!.element.parentElement?.className).toContain('text-xs')
  })

  it('renders the pelican document and no extra text on the card', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan({ test_type: 'pelican' })],
        [makeResult({ raw_html: '<svg><rect/></svg>', note: '脚踩空' })]
      )
    )

    const frame = wrapper.find('iframe[title="pelican probe preview"]')
    expect(frame.exists()).toBe(true)
    expect(frame.attributes('srcdoc')).toBe('<svg><rect/></svg>')

    const card = wrapper.find('article')
    expect(card.text()).toContain('admin.pelicanProbe.statusNormal')
    expect(card.text()).not.toContain('脚踩空')
    expect(card.find('pre').exists()).toBe(false)
    // 鹈鹕卡片 = 头部两行 + 预览行，预览只走 iframe
    expect(card.element.children).toHaveLength(2)
  })

  // 用户明确要求：糖果卡片只给出「未判为 21 ✗」这类判定，其余一律不显示。
  it('shows candy cards as the verdict only — no answer, no judge note', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan({ test_type: 'candy' })],
        [
          makeResult({
            raw_html: '我先想一下……答案是 21',
            verdict: 'degraded',
            note: 'judge=deepseek-v4.1-flash: NO'
          })
        ]
      )
    )

    const card = wrapper.find('article')
    expect(card.text()).toContain('admin.pelicanProbe.candyVerdictDegraded')
    expect(card.text()).toContain('gpt-5')
    expect(card.text()).not.toContain('答案是 21')
    expect(card.text()).not.toContain('deepseek-v4.1-flash')
    expect(card.text()).not.toContain('admin.pelicanProbe.candyAnswerLabel')
    expect(card.find('pre').exists()).toBe(false)
    expect(card.find('iframe').exists()).toBe(false)
    // 不渲染任何正文 => 卡片只有那两行头部，因此非常矮
    expect(card.element.children).toHaveLength(1)
    expect(card.html()).not.toContain('self-stretch')
  })

  it('keeps candy history cards to the verdict pill only', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan({ test_type: 'candy' })],
        [
          makeResult({ id: 103, verdict: 'pass', created_at: '2026-01-03T00:00:00Z' }),
          makeResult({
            id: 102,
            verdict: 'degraded',
            raw_html: '答案是 21',
            note: 'judge=deepseek-v4.1-flash: NO',
            created_at: '2026-01-02T00:00:00Z'
          })
        ]
      )
    )

    await clickHistoryToggle(wrapper)
    await flushPromises()

    const html = wrapper.html()
    expect(html).not.toContain('答案是 21')
    expect(html).not.toContain('deepseek-v4.1-flash')
    expect(wrapper.findAll('pre')).toHaveLength(0)
    expect(wrapper.findAll('iframe')).toHaveLength(0)
    // 判定仍然可见：历史卡片头部的小徽标
    expect(wrapper.text()).toContain('admin.pelicanProbe.candyVerdictDegraded')
  })

  // 用户反馈：「等以后测试多了，这里会不会有问题呢」「图二这种形式叫什么，
  // 更多用这个表示会更好吗」。下面这组用例锁定补上的聚合摘要 + 紧凑趋势条。

  it('summarises recent runs, keeping failed runs out of the pass rate', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan()],
        [
          makeResult({ id: 104, verdict: 'pass', created_at: '2026-01-04T00:00:00Z' }),
          makeResult({ id: 103, verdict: 'degraded', created_at: '2026-01-03T00:00:00Z' }),
          // 上游错误（并发超限 / 超时）：不是模型质量信号，进分母会把
          // 2/3 报成 2/4，等于拿网关限流给模型扣分。
          makeResult({
            id: 102,
            status: 'failed',
            verdict: 'pending',
            created_at: '2026-01-02T00:00:00Z'
          }),
          makeResult({ id: 101, verdict: 'pass', created_at: '2026-01-01T00:00:00Z' })
        ]
      )
    )

    expect(wrapper.text()).toContain('admin.pelicanProbe.passRate')
    // pass 2 / judged 3 = 67%（不是 50%）
    expect(wrapper.text()).toContain('67%')
    expect(wrapper.text()).toContain('admin.pelicanProbe.avgLatency')
    expect(wrapper.text()).toContain('admin.pelicanProbe.runCount')
  })

  it('draws the trend strip oldest first, so it reads like a timeline', async () => {
    const wrapper = await mountView(
      enabledStatus(
        [makePlan()],
        [
          makeResult({ id: 103, verdict: 'pass', created_at: '2026-01-03T00:00:00Z' }),
          makeResult({ id: 102, verdict: 'degraded', created_at: '2026-01-02T00:00:00Z' }),
          makeResult({ id: 101, verdict: 'pass', created_at: '2026-01-01T00:00:00Z' })
        ]
      )
    )

    const cells = wrapper.find('[role="img"]').findAll('span')
    expect(cells).toHaveLength(3)
    // 最左是最旧那次：pass → degraded → pass
    expect(cells[0].classes()).toContain('bg-emerald-500')
    expect(cells[1].classes()).toContain('bg-rose-500')
    expect(cells[2].classes()).toContain('bg-emerald-500')
  })

  it('caps the trend strip so a long-lived plan cannot stretch the card', async () => {
    const results = Array.from({ length: 40 }, (_, i) =>
      makeResult({
        id: 100 + i,
        created_at: new Date(Date.UTC(2026, 0, 1, 0, i)).toISOString()
      })
    )

    const wrapper = await mountView(enabledStatus([makePlan()], results))

    // 固定宽度：40 次探测仍然只画 20 格，卡片高度不随历史增长。
    expect(wrapper.find('[role="img"]').findAll('span')).toHaveLength(20)
  })

  it('hides the aggregate row when there is nothing but the latest run', async () => {
    const wrapper = await mountView(enabledStatus([makePlan()], [makeResult()]))

    // 一格不构成趋势，胶囊已经说明了状态。
    expect(wrapper.find('[role="img"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('admin.pelicanProbe.passRate')
  })

  it('shows only the newest few history entries until the rest is requested', async () => {
    const results = Array.from({ length: 12 }, (_, i) =>
      makeResult({
        id: 100 + i,
        created_at: new Date(Date.UTC(2026, 0, 1, 0, i)).toISOString()
      })
    )

    const wrapper = await mountView(enabledStatus([makePlan()], results))
    await clickHistoryToggle(wrapper)
    await flushPromises()

    // 12 条结果里最新那条是卡片本体，剩 11 条历史默认只展开 5 条。
    expect(wrapper.findAll('iframe[title="pelican probe history preview"]')).toHaveLength(5)
    expect(wrapper.text()).toContain('admin.pelicanProbe.showAllHistory')

    const showAll = wrapper
      .findAll('button')
      .find((b) => b.text().includes('admin.pelicanProbe.showAllHistory'))
    expect(showAll, 'show-all button should exist').toBeTruthy()
    await showAll!.trigger('click')
    await flushPromises()

    expect(wrapper.findAll('iframe[title="pelican probe history preview"]')).toHaveLength(11)
  })
})
