import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const mocks = vi.hoisted(() => {
  const instance = {
    destroy: vi.fn(),
    drive: vi.fn(),
    isActive: vi.fn(() => true),
    moveNext: vi.fn(),
    movePrevious: vi.fn(),
    getActiveIndex: vi.fn(() => 0),
    getActiveElement: vi.fn(() => null),
  }
  return { driver: vi.fn(() => instance), instance }
})

vi.mock('driver.js', () => ({ driver: mocks.driver }))
vi.mock('driver.js/dist/driver.css', () => ({}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 1, role: 'admin' }, isSimpleMode: false }),
}))
vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    getDriverInstance: () => null,
    setDriverInstance: vi.fn(),
    setControlMethods: vi.fn(),
    clearControlMethods: vi.fn(),
    isDriverActive: () => false,
  }),
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ locale: { value: 'zh' }, t: (key: string) => key }) }
})

import { useOnboardingTour } from '../useOnboardingTour'
import { getAdminSteps, getUserSteps } from '@/components/Guide/steps'

const STORAGE_KEY = 'admin_guide_1_admin_v4_interactive'

function mountTour() {
  let tour: ReturnType<typeof useOnboardingTour> | null = null
  const Host = defineComponent({
    setup() {
      tour = useOnboardingTour({ storageKey: 'admin_guide', autoStart: false })
      return () => h('div')
    },
  })
  const wrapper = mount(Host)
  return { wrapper, tour: tour as unknown as ReturnType<typeof useOnboardingTour> }
}

describe('onboarding welcome step "skip"', () => {
  beforeEach(() => {
    localStorage.clear()
    mocks.driver.mockClear()
    Object.values(mocks.instance).forEach((mock) => mock.mockClear())
  })

  it('keeps the skip button clickable on the first step of both tours', () => {
    // driver.js 默认在第一步给 popover 注入 disableButtons: ["previous"]，
    // 而「跳过」正是复用该按钮；两个欢迎步骤都必须显式覆盖掉这个禁用。
    const t = (key: string) => key
    for (const steps of [getAdminSteps(t), getUserSteps(t)]) {
      expect(steps[0].popover?.disableButtons).toEqual([])
      expect(steps[0].popover?.prevBtnText).toContain('welcome.prevBtn')
    }
  })

  it('ends the tour and remembers it when the first step skip is clicked', async () => {
    const { tour } = mountTour()
    await tour.startTour()
    await flushPromises()

    const config = mocks.driver.mock.calls[0][0] as {
      onPrevClick: (el: unknown, step: unknown, opts: { state: { activeIndex: number } }) => void
    }
    expect(config).toBeTruthy()

    config.onPrevClick(null, null, { state: { activeIndex: 0 } })

    expect(mocks.instance.destroy).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem(STORAGE_KEY)).toBe('true')
  })

  it('still walks back a step when skip is not the active step', async () => {
    const { tour } = mountTour()
    await tour.startTour()
    await flushPromises()

    const config = mocks.driver.mock.calls[0][0] as {
      onPrevClick: (el: unknown, step: unknown, opts: { state: { activeIndex: number } }) => void
    }
    config.onPrevClick(null, null, { state: { activeIndex: 3 } })

    expect(mocks.instance.movePrevious).toHaveBeenCalledTimes(1)
    expect(mocks.instance.destroy).not.toHaveBeenCalled()
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
  })
})
