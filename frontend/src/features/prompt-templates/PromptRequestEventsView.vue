<template>
  <AppLayout>
    <div class="mx-auto max-w-[1500px] pb-10">
      <header class="mb-5 flex flex-wrap items-end justify-between gap-4">
        <div class="min-w-0">
          <h1 class="text-[22px] font-semibold tracking-tight text-gray-950 dark:text-white">
            {{ t('admin.promptTemplates.events.title') }}
          </h1>
          <p class="mt-1.5 max-w-2xl text-sm leading-relaxed text-gray-500 dark:text-dark-300">
            {{ t('admin.promptTemplates.events.description') }}
          </p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">
          {{ t('admin.promptTemplates.list.refresh') }}
        </button>
      </header>

      <div v-if="loadError" role="alert" class="card p-6">
        <p class="text-sm text-red-700 dark:text-red-300">{{ loadError }}</p>
        <button type="button" class="btn btn-secondary btn-sm mt-3" @click="load">
          {{ t('admin.promptTemplates.reload') }}
        </button>
      </div>

      <div v-else class="card">
        <!-- 筛选即查询：工具栏贴在表头之上，改完直接回车/点查询 -->
        <div class="border-b border-gray-100 bg-gray-50/60 px-4 py-3 dark:border-dark-700 dark:bg-dark-700/20 sm:px-6">
          <RequestEventFilterBar :loading="loading" @search="handleSearch" />
        </div>

        <div class="card-body">
          <p class="mb-3 text-[11px] leading-relaxed text-gray-400 dark:text-dark-500">
            {{ t('admin.promptTemplates.events.appliedNotice') }}
          </p>

          <DataTable
            :columns="columns"
            :data="rows"
            :loading="loading"
            row-key="id"
            :estimate-row-height="64"
          >
            <template #cell-requestId="{ row }">
              <div class="flex items-center gap-1.5">
                <span class="max-w-[9rem] truncate font-mono text-xs text-gray-700 dark:text-dark-200" :title="row.event.request_id">
                  {{ row.event.request_id || t('admin.promptTemplates.events.notRecorded') }}
                </span>
                <span
                  v-if="row.attempt > 1"
                  class="rounded bg-gray-100 px-1 py-0.5 text-[10px] tabular-nums text-gray-500 dark:bg-dark-700 dark:text-dark-300"
                  :title="t('admin.promptTemplates.events.columns.attempt')"
                >
                  #{{ row.attempt }}
                </span>
              </div>
            </template>

            <template #cell-group="{ row }">
              <span class="text-xs text-gray-700 dark:text-dark-200">
                {{ row.group || t('admin.promptTemplates.events.notRecorded') }}
                <span v-if="row.account" class="text-gray-400 dark:text-dark-500">· {{ row.account }}</span>
              </span>
            </template>

            <template #cell-clientModel="{ row }">
              <span class="block max-w-[16rem] truncate font-mono text-xs text-gray-700 dark:text-dark-200" :title="`${row.clientModel} → ${row.upstreamModel}`">
                {{ row.clientModel || t('admin.promptTemplates.events.notRecorded') }}
                <span v-if="row.upstreamModel" class="text-gray-400 dark:text-dark-500">→ {{ row.upstreamModel }}</span>
              </span>
            </template>

            <template #cell-profile="{ row }">
              <span
                v-if="row.event.outbound_profile"
                class="font-mono text-xs text-gray-700 dark:text-dark-200"
                :title="t(`admin.promptTemplates.profiles.${promptProfileKey(row.event.outbound_profile)}`)"
              >
                {{ row.event.outbound_profile }}
              </span>
              <span v-else class="text-gray-400 dark:text-dark-500">{{ t('admin.promptTemplates.events.notRecorded') }}</span>
            </template>

            <template #cell-bindingSource="{ row }">
              <span class="text-xs text-gray-700 dark:text-dark-200">
                {{ row.event.binding_source ? t(`admin.promptTemplates.bindingSources.${bindingSourceKey(row.event.binding_source)}`) : t('admin.promptTemplates.events.notRecorded') }}
                <span v-if="row.event.version_id" class="font-mono text-gray-400 dark:text-dark-500">
                  · {{ t('admin.promptTemplates.events.versionValue', { id: row.event.version_id }) }}
                </span>
              </span>
            </template>

            <template #cell-applied="{ row }">
              <span class="inline-flex items-center gap-1.5">
                <span :class="['badge', row.event.applied ? 'badge-success' : 'badge-gray']">
                  {{ row.event.applied ? t('admin.promptTemplates.events.appliedYes') : t('admin.promptTemplates.events.appliedNo') }}
                </span>
                <span
                  v-if="row.event.reason && row.event.reason !== 'applied'"
                  class="max-w-[10rem] truncate text-[11px] text-gray-500 dark:text-dark-400"
                  :title="row.event.reason"
                >
                  {{ reasonLabel(row.event.reason) }}
                </span>
              </span>
            </template>

            <template #cell-addedBytes="{ row }">
              <span class="font-mono text-xs tabular-nums text-gray-700 dark:text-dark-200">{{ row.addedBytes }}</span>
            </template>

            <template #cell-duration="{ row }">
              <span class="text-xs tabular-nums text-gray-600 dark:text-dark-300">{{ row.duration }}</span>
            </template>

            <template #empty>
              <EmptyState :title="t('admin.promptTemplates.events.empty')" :description="t('admin.promptTemplates.events.description')" />
            </template>
          </DataTable>
        </div>

        <div v-if="total > 0" class="card-footer">
          <Pagination
            :total="total"
            :page="page"
            :page-size="pageSize"
            @update:page="handlePageChange"
            @update:page-size="handlePageSizeChange"
          />
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Pagination from '@/components/common/Pagination.vue'
import type { Column } from '@/components/common/types'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { extractI18nErrorMessage } from '@/utils/apiError'
import RequestEventFilterBar from './components/RequestEventFilterBar.vue'
import { listRequestEvents } from './api'
import type { PromptRequestEvent, PromptRequestEventQuery } from './types'
import { bindingSourceKey, promptProfileKey, promptReasonKey } from './viewModel'
import { formatDateTime } from './format'

interface EventRow {
  id: number
  event: PromptRequestEvent
  group: string
  account: string
  clientModel: string
  upstreamModel: string
  attempt: number
  addedBytes: number
  duration: string
  createdAt: string
}

const { t, locale } = useI18n()

const loading = ref(false)
const loadError = ref('')
const events = ref<PromptRequestEvent[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(getPersistedPageSize())
/** 只保留筛选条件；分页参数由下方的分页控件单独维护。 */
const filters = ref<PromptRequestEventQuery>({})

/**
 * 列做过合并：分组/账号、客户端→上游模型、来源/版本、结果（注入与否 + 原因）
 * 各占一格，14 列压到 9 列，横向滚动明显减少。
 */
const columns = computed<Column[]>(() => [
  { key: 'createdAt', label: t('admin.promptTemplates.events.columns.createdAt'), sortable: true },
  { key: 'requestId', label: t('admin.promptTemplates.events.columns.requestId') },
  { key: 'group', label: t('admin.promptTemplates.events.columns.groupAccount'), sortable: true },
  { key: 'clientModel', label: t('admin.promptTemplates.events.columns.model'), sortable: true },
  { key: 'profile', label: t('admin.promptTemplates.events.columns.profile') },
  { key: 'bindingSource', label: t('admin.promptTemplates.events.columns.sourceVersion') },
  { key: 'applied', label: t('admin.promptTemplates.events.columns.result'), sortable: true },
  { key: 'addedBytes', label: t('admin.promptTemplates.events.columns.addedBytes'), sortable: true },
  { key: 'duration', label: t('admin.promptTemplates.events.columns.duration'), sortable: true },
])

/** 固定原因枚举 → 可读文案；未知值回退到 unknown，绝不臆造。 */
function reasonLabel(reason: string): string {
  return t(`admin.promptTemplates.reasons.${promptReasonKey(reason)}`)
}

const rows = computed<EventRow[]>(() =>
  events.value.map((event) => ({
    id: event.id,
    event,
    attempt: event.attempt_no,
    group: event.group_id ? t('admin.promptTemplates.events.groupValue', { id: event.group_id }) : '',
    account: event.account_id ? t('admin.promptTemplates.events.accountValue', { id: event.account_id }) : '',
    clientModel: event.client_model ?? '',
    upstreamModel: event.upstream_model ?? '',
    addedBytes: event.added_bytes,
    duration: t('admin.promptTemplates.events.durationMs', { ms: event.apply_duration_ms }),
    createdAt: formatDateTime(event.created_at, locale.value),
  })),
)

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const result = await listRequestEvents({
      ...filters.value,
      page: page.value,
      page_size: pageSize.value,
    })
    events.value = result.items
    total.value = result.total
  } catch (error) {
    events.value = []
    total.value = 0
    loadError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.events.loadFailed'),
    )
  } finally {
    loading.value = false
  }
}

function handleSearch(query: PromptRequestEventQuery) {
  filters.value = query
  page.value = 1
  void load()
}

function handlePageChange(next: number) {
  page.value = next
  void load()
}

function handlePageSizeChange(next: number) {
  pageSize.value = next
  page.value = 1
  void load()
}

onMounted(load)
</script>
