import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const mocks = vi.hoisted(() => ({ listRequestEvents: vi.fn(), tKeys: [] as string[] }))

vi.mock('../api', () => ({ listRequestEvents: mocks.listRequestEvents }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: { value: 'en' },
      t: (key: string, params?: Record<string, unknown>) => {
        mocks.tKeys.push(key)
        return key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? `{${token}}`))
      },
    }),
  }
})

import PromptRequestEventsView from '../PromptRequestEventsView.vue'

const AppLayoutStub = { template: '<div><slot /></div>' }
const IconStub = { template: '<i />' }
const DataTableStub = defineComponent({
  props: ['columns', 'data', 'loading'],
  template: '<div data-test="table"><span data-test="row-count">{{ data.length }}</span><slot name="empty" /></div>',
})
const EmptyStateStub = defineComponent({
  props: ['title', 'description'],
  template: '<div data-test="empty-state">{{ title }}</div>',
})
const PaginationStub = defineComponent({
  props: ['total', 'page', 'pageSize'],
  emits: ['update:page', 'update:pageSize'],
  template: '<div data-test="pagination">{{ total }}|{{ page }}|{{ pageSize }}</div>',
})
const FilterBarStub = defineComponent({
  props: ['loading'],
  emits: ['search'],
  template: '<button data-test="filter-search" @click="$emit(\'search\', { group_id: 7 })">search</button>',
})

function mountView() {
  return mount(PromptRequestEventsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Icon: IconStub,
        DataTable: DataTableStub,
        EmptyState: EmptyStateStub,
        Pagination: PaginationStub,
        RequestEventFilterBar: FilterBarStub,
      },
    },
  })
}

function envelope(items: unknown[], total: number, page = 1, pageSize = 20) {
  return { items, total, page, page_size: pageSize, pages: Math.max(1, Math.ceil(total / pageSize)) }
}

const oneEvent = {
  id: 11,
  attempt_no: 1,
  group_id: 7,
  applied: true,
  reason: 'applied',
  added_bytes: 111,
  apply_duration_ms: 2,
  created_at: '2026-10-08T06:00:00Z',
}

describe('PromptRequestEventsView pagination', () => {
  beforeEach(() => {
    mocks.listRequestEvents.mockReset()
    mocks.tKeys.length = 0
    mocks.listRequestEvents.mockResolvedValue(envelope([oneEvent], 41))
  })

  it('requests the first page and renders the envelope total', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(mocks.listRequestEvents).toHaveBeenCalledWith(
      expect.objectContaining({ page: 1, page_size: expect.any(Number) }),
    )
    expect(wrapper.find('[data-test="row-count"]').text()).toBe('1')
    expect(wrapper.find('[data-test="pagination"]').text()).toContain('41|1|')
  })

  it('requests the selected page while keeping the page size', async () => {
    const wrapper = mountView()
    await flushPromises()
    const first = mocks.listRequestEvents.mock.calls[0][0]

    wrapper.findComponent(PaginationStub).vm.$emit('update:page', 3)
    await flushPromises()

    expect(mocks.listRequestEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: 3, page_size: first.page_size }),
    )
  })

  it('resets to page 1 when the page size changes', async () => {
    const wrapper = mountView()
    await flushPromises()

    wrapper.findComponent(PaginationStub).vm.$emit('update:pageSize', 50)
    await flushPromises()

    expect(mocks.listRequestEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: 1, page_size: 50 }),
    )
  })

  it('resets to page 1 and keeps the filters on a new search', async () => {
    const wrapper = mountView()
    await flushPromises()

    wrapper.findComponent(PaginationStub).vm.$emit('update:page', 2)
    await flushPromises()
    await wrapper.find('[data-test="filter-search"]').trigger('click')
    await flushPromises()

    expect(mocks.listRequestEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({ group_id: 7, page: 1 }),
    )
  })

  it('shows the failure and never claims the list is empty when the load fails', async () => {
    mocks.listRequestEvents.mockRejectedValue({ status: 423, code: 'ADMIN_COMPLIANCE_ACK_REQUIRED', message: 'locked' })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[role="alert"]').text()).toContain('locked')
    expect(wrapper.find('[data-test="table"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('admin.promptTemplates.events.empty')
    // 后端错误码先走 i18n 映射，映射不到才回退到后端原文。
    expect(mocks.tKeys).toContain('admin.promptTemplates.errors.ADMIN_COMPLIANCE_ACK_REQUIRED')
  })
})
