import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import type { PelicanProbePlan, PelicanProbeResult } from '@/types'
import PelicanProbeView from '@/views/admin/PelicanProbeView.vue'

// 管理端「立即探测」异步契约适配：
// - 新契约：POST .../run 立刻 202 + {started, plan_id}，探测在后台跑
// - 旧契约：响应体里直接带结果
// 两种都要能跑：任何 2xx 都算「已启动」，然后轮询结果列表。

const {
  showSuccess,
  showError,
  listPlans,
  listRecent,
  listResults,
  runNow,
  getGlobalStatus,
  groupsGetAll
} = vi.hoisted(() => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
  listPlans: vi.fn(),
  listRecent: vi.fn(),
  listResults: vi.fn(),
  runNow: vi.fn(),
  getGlobalStatus: vi.fn(),
  groupsGetAll: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { getAll: groupsGetAll },
    pelicanProbe: {
      listPlans,
      listRecent,
      listResults,
      runNow,
      getGlobalStatus,
      setGlobalStatus: vi.fn(),
      createPlan: vi.fn(),
      updatePlan: vi.fn(),
      deletePlan: vi.fn(),
      reviewResult: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  BaseDialog: { props: ['show', 'title', 'width'], template: '<div />' },
  ConfirmDialog: { props: ['show', 'title', 'message'], template: '<div />' },
  Select: { props: ['modelValue', 'options'], template: '<div />' },
  Input: { props: ['modelValue'], template: '<input />' },
  Toggle: { props: ['modelValue'], template: '<span />' }
}

const PLAN: PelicanProbePlan = {
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
  updated_at: '2026-01-01T00:00:00Z'
}

const OLD_RESULT: PelicanProbeResult = {
  id: 1,
  plan_id: 1,
  status: 'success',
  raw_html: '<html></html>',
  verdict: 'pass',
  note: '',
  reviewed_by: '',
  reviewed_at: null,
  error_message: '',
  latency_ms: 1000,
  prompt_tokens: 1,
  completion_tokens: 1,
  started_at: '2026-01-01T00:00:00Z',
  finished_at: '2026-01-01T00:00:01Z',
  created_at: '2026-01-01T00:00:01Z'
}

const NEW_RESULT: PelicanProbeResult = { ...OLD_RESULT, id: 2, created_at: '2026-01-02T00:00:01Z' }

/** 组件的 await 链全部走微任务；假定时器下不能用 flushPromises（它依赖 setTimeout）。 */
async function settle(times = 8) {
  for (let i = 0; i < times; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

/** 行内「立即探测」按钮：启动后文案会变成「探测中」。 */
function runControl(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('button').find((b) => {
    const text = b.text().trim()
    return text === 'admin.pelicanProbe.runNow' || text === 'admin.pelicanProbe.running'
  })
}

function isPolling(wrapper: ReturnType<typeof mount>) {
  return wrapper.text().includes('admin.pelicanProbe.probing')
}

async function mountView() {
  const wrapper = mount(PelicanProbeView, { global: { stubs } })
  await settle()
  return wrapper
}

describe('PelicanProbeView run-now async contract', () => {
  let wrapper: ReturnType<typeof mount> | null = null

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    vi.clearAllMocks()
    listPlans.mockResolvedValue([PLAN])
    listRecent.mockResolvedValue([OLD_RESULT])
    listResults.mockResolvedValue([OLD_RESULT])
    groupsGetAll.mockResolvedValue([])
    getGlobalStatus.mockResolvedValue({ enabled: true })
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = null
    vi.useRealTimers()
  })

  it('treats the new 202 acknowledgement as started and starts polling', async () => {
    runNow.mockResolvedValue({ started: true, plan_id: 1 })
    wrapper = await mountView()

    await runControl(wrapper)!.trigger('click')
    await settle()

    expect(runNow).toHaveBeenCalledWith(1)
    expect(showSuccess).toHaveBeenCalledWith('admin.pelicanProbe.runStarted')
    expect(showError).not.toHaveBeenCalled()

    // 轮询期间：按钮禁用并显示「探测中」，结果墙显示探测中徽标
    expect(runControl(wrapper)!.text().trim()).toBe('admin.pelicanProbe.running')
    expect(runControl(wrapper)!.attributes('disabled')).toBeDefined()
    expect(isPolling(wrapper)).toBe(true)
  })

  it('still accepts the legacy synchronous contract that returns a result body', async () => {
    runNow.mockResolvedValue(OLD_RESULT)
    wrapper = await mountView()

    await runControl(wrapper)!.trigger('click')
    await settle()

    expect(showSuccess).toHaveBeenCalledWith('admin.pelicanProbe.runStarted')
    expect(showError).not.toHaveBeenCalled()
    expect(isPolling(wrapper)).toBe(true)
  })

  it('stops polling as soon as a result newer than the click shows up', async () => {
    runNow.mockResolvedValue({ started: true, plan_id: 1 })
    listResults.mockReset()
    listResults
      .mockResolvedValueOnce([OLD_RESULT]) // 点击前的基线快照
      .mockResolvedValue([NEW_RESULT, OLD_RESULT]) // 轮询
    wrapper = await mountView()

    await runControl(wrapper)!.trigger('click')
    await settle()
    expect(isPolling(wrapper)).toBe(true)

    await vi.advanceTimersByTimeAsync(10_000)
    await settle()

    expect(isPolling(wrapper)).toBe(false)
    expect(runControl(wrapper)!.text().trim()).toBe('admin.pelicanProbe.runNow')
    expect(runControl(wrapper)!.attributes('disabled')).toBeUndefined()
  })

  it('gives up polling after three minutes when nothing new arrives', async () => {
    runNow.mockResolvedValue({ started: true, plan_id: 1 })
    wrapper = await mountView()

    await runControl(wrapper)!.trigger('click')
    await settle()

    // 2 分钟后仍在轮询
    for (let i = 0; i < 12; i++) {
      await vi.advanceTimersByTimeAsync(10_000)
      await settle()
    }
    expect(isPolling(wrapper)).toBe(true)

    // 越过 3 分钟上限后停止
    for (let i = 0; i < 8; i++) {
      await vi.advanceTimersByTimeAsync(10_000)
      await settle()
    }
    expect(isPolling(wrapper)).toBe(false)
    expect(runControl(wrapper)!.attributes('disabled')).toBeUndefined()
  })

  it('keeps waiting after the legacy 30s axios timeout', async () => {
    runNow.mockRejectedValue({ status: 0, code: 'ECONNABORTED' })
    wrapper = await mountView()

    await runControl(wrapper)!.trigger('click')
    await settle()

    expect(showError).toHaveBeenCalledWith('admin.pelicanProbe.runTimeout')
    expect(isPolling(wrapper)).toBe(true)
  })

  it('reports a real failure with extractApiErrorMessage and does not poll', async () => {
    runNow.mockRejectedValue({ status: 500, message: 'probe plan not found' })
    wrapper = await mountView()

    await runControl(wrapper)!.trigger('click')
    await settle()

    expect(showError).toHaveBeenCalledWith('probe plan not found')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(isPolling(wrapper)).toBe(false)
    expect(runControl(wrapper)!.text().trim()).toBe('admin.pelicanProbe.runNow')
  })
})

// 探测本身失败 = 没有任何模型输出可评审。给它配通过/降智按钮，等于把一次
// 上游限流点成质量结论；把它算进「待评审」计数，等于让人去处理一件不存在的事。
describe('PelicanProbeView review queue', () => {
  let wrapper: ReturnType<typeof mount> | null = null

  beforeEach(() => {
    vi.clearAllMocks()
    listPlans.mockResolvedValue([PLAN])
    getGlobalStatus.mockResolvedValue({ enabled: true })
    groupsGetAll.mockResolvedValue([])
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = null
  })

  const FAILED: PelicanProbeResult = {
    ...OLD_RESULT,
    id: 9,
    status: 'failed',
    // 新值出现之前落库的行长这样；它和新的 invalid 一样不可评审。
    verdict: 'pending',
    raw_html: '',
    error_message: 'gateway_concurrency_limit'
  }

  const findButton = (w: ReturnType<typeof mount>, key: string) =>
    w.findAll('button').find((b) => b.text().includes(key))

  it('hides the review buttons for a failed result but keeps the source view', async () => {
    listRecent.mockResolvedValue([FAILED])
    listResults.mockResolvedValue([FAILED])

    wrapper = await mountView()
    await settle()

    expect(wrapper.text()).toContain('admin.pelicanProbe.reviewNotApplicable')
    expect(findButton(wrapper, 'admin.pelicanProbe.markPass')).toBeUndefined()
    expect(findButton(wrapper, 'admin.pelicanProbe.markDegraded')).toBeUndefined()
    // 诊断入口必须留着：看不到源码就没法判断这次失败是什么。
    expect(findButton(wrapper, 'admin.pelicanProbe.viewSource')).toBeTruthy()
  })

  it('keeps the review buttons for a result that actually rendered', async () => {
    listRecent.mockResolvedValue([OLD_RESULT])
    listResults.mockResolvedValue([OLD_RESULT])

    wrapper = await mountView()
    await settle()

    expect(findButton(wrapper, 'admin.pelicanProbe.markPass')).toBeTruthy()
    expect(findButton(wrapper, 'admin.pelicanProbe.markDegraded')).toBeTruthy()
    expect(wrapper.text()).not.toContain('admin.pelicanProbe.reviewNotApplicable')
  })

  it('counts a failed result as invalid instead of pending review', async () => {
    listRecent.mockResolvedValue([OLD_RESULT, FAILED])
    listResults.mockResolvedValue([OLD_RESULT, FAILED])

    wrapper = await mountView()
    await settle()

    // 两张统计卡片都要渲染出来；数字本身靠相邻文案定位不可靠，这里断言
    // 「无效」卡片存在，并且失败记录没有混进待评审文案块。
    expect(wrapper.text()).toContain('admin.pelicanProbe.statsInvalid')
    expect(wrapper.text()).toContain('admin.pelicanProbe.statsPending')
  })
})
