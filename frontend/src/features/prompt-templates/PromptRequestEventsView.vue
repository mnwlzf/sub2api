<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] pb-8">
      <header class="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 class="text-2xl font-semibold tracking-tight text-gray-950 dark:text-white">
            {{ t('admin.promptTemplates.events.title') }}
          </h1>
          <p class="mt-2 max-w-3xl text-sm text-gray-500 dark:text-dark-300">
            {{ t('admin.promptTemplates.events.description') }}
          </p>
        </div>
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="load">
          {{ t('admin.promptTemplates.list.refresh') }}
        </button>
      </header>

      <div
        role="status"
        class="mb-4 flex items-start gap-2.5 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200"
      >
        <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" />
        <span>{{ t('admin.promptTemplates.events.appliedNotice') }}</span>
      </div>

      <div class="card mb-4 p-4 sm:p-6">
        <RequestEventFilterBar :loading="loading" @search="handleSearch" />
      </div>

      <div v-if="loadError" role="alert" class="mb-4 rounded-xl border border-red-200 bg-red-50 p-5 dark:border-red-900 dark:bg-red-950/30">
        <p class="text-sm text-red-700 dark:text-red-300">{{ loadError }}</p>
        <button type="button" class="btn btn-secondary btn-sm mt-3" @click="load">
          {{ t('admin.promptTemplates.reload') }}
        </button>
      </div>

      <div v-else class="card p-4 sm:p-6">
        <DataTable
          :columns="columns"
          :data="rows"
          :loading="loading"
          row-key="id"
          :estimate-row-height="64"
        >
          <template #cell-requestId="{ row }">
            <span class="font-mono text-xs text-gray-700 dark:text-dark-200" :title="row.event.request_id">
              {{ row.event.request_id || t('admin.promptTemplates.events.notRecorded') }}
            </span>
          </template>

          <template #cell-applied="{ row }">
            <span :class="['badge', row.event.applied ? 'badge-success' : 'badge-gray']">
              {{ row.event.applied ? t('admin.promptTemplates.events.appliedYes') : t('admin.promptTemplates.events.appliedNo') }}
            </span>
          </template>

          <template #cell-reason="{ row }">
            <span class="text-gray-700 dark:text-dark-200">{{ reasonLabel(row.event.reason) }}</span>
            <span class="ml-1 font-mono text-xs text-gray-400 dark:text-dark-500">{{ row.event.reason }}</span>
          </template>

          <template #cell-profile="{ row }">
            <span v-if="row.event.outbound_profile" class="text-gray-700 dark:text-dark-200">
              {{ t(`admin.promptTemplates.profiles.${promptProfileKey(row.event.outbound_profile)}`) }}
            </span>
            <span v-else class="text-gray-400 dark:text-dark-500">{{ t('admin.promptTemplates.events.notRecorded') }}</span>
          </template>

          <template #cell-bindingSource="{ row }">
            <span v-if="row.event.binding_source" class="text-gray-700 dark:text-dark-200">
              {{ t(`admin.promptTemplates.bindingSources.${bindingSourceKey(row.event.binding_source)}`) }}
            </span>
            <span v-else class="text-gray-400 dark:text-dark-500">{{ t('admin.promptTemplates.events.notRecorded') }}</span>
          </template>

          <template #cell-version="{ row }">
            <span v-if="row.event.version_id" class="font-mono text-xs text-gray-700 dark:text-dark-200">
              {{ t('admin.promptTemplates.events.versionValue', { id: row.event.version_id }) }}
            </span>
            <span v-else class="text-gray-400 dark:text-dark-500">{{ t('admin.promptTemplates.events.notRecorded') }}</span>
          </template>

          <template #empty>
            <EmptyState :title="t('admin.promptTemplates.events.empty')" :description="t('admin.promptTemplates.events.description')" />
          </template>
        </DataTable>

        <Pagination
          v-if="total > 0"
          :total="total"
          :page="page"
          :page-size="pageSize"
          @update:page="handlePageChange"
          @update:page-size="handlePageSizeChange"
        />
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
import Icon from '@/components/icons/Icon.vue'
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

const columns = computed<Column[]>(() => [
  { key: 'requestId', label: t('admin.promptTemplates.events.columns.requestId') },
  { key: 'attempt', label: t('admin.promptTemplates.events.columns.attempt'), sortable: true },
  { key: 'group', label: t('admin.promptTemplates.events.columns.group'), sortable: true },
  { key: 'account', label: t('admin.promptTemplates.events.columns.account'), sortable: true },
  { key: 'clientModel', label: t('admin.promptTemplates.events.columns.clientModel'), sortable: true },
  { key: 'upstreamModel', label: t('admin.promptTemplates.events.columns.upstreamModel'), sortable: true },
  { key: 'profile', label: t('admin.promptTemplates.events.columns.profile') },
  { key: 'bindingSource', label: t('admin.promptTemplates.events.columns.bindingSource') },
  { key: 'version', label: t('admin.promptTemplates.events.columns.version') },
  { key: 'applied', label: t('admin.promptTemplates.events.columns.applied'), sortable: true },
  { key: 'reason', label: t('admin.promptTemplates.events.columns.reason') },
  { key: 'addedBytes', label: t('admin.promptTemplates.events.columns.addedBytes'), sortable: true },
  { key: 'duration', label: t('admin.promptTemplates.events.columns.duration'), sortable: true },
  { key: 'createdAt', label: t('admin.promptTemplates.events.columns.createdAt'), sortable: true },
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
