<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-6">
      <div class="overflow-hidden rounded-[28px] border border-white/80 bg-gradient-to-br from-sky-50 via-white to-emerald-50 shadow-[0_24px_60px_-28px_rgba(15,23,42,0.35)] dark:border-dark-700 dark:from-dark-800 dark:via-dark-800 dark:to-dark-700">
        <div class="border-b border-white/70 bg-white/70 px-6 py-5 backdrop-blur dark:border-dark-700/80 dark:bg-dark-800/80">
          <div class="flex flex-wrap items-start justify-between gap-4">
            <div>
              <h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('admin.scheduledJobs.title') }}</h2>
              <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">{{ t('admin.scheduledJobs.description') }}</p>
            </div>
            <div class="flex gap-2">
              <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="loadJobs">
                {{ loading ? t('common.loading') : t('common.refresh') }}
              </button>
              <button type="button" class="btn btn-primary btn-sm" @click="openCreate">
                {{ t('admin.scheduledJobs.create') }}
              </button>
            </div>
          </div>
        </div>

        <div class="px-4 py-5 sm:px-6">
          <div v-if="jobs.length" class="space-y-4">
            <div
              v-for="job in jobs"
              :key="job.id"
              class="rounded-3xl border border-gray-200/80 bg-white/95 p-5 shadow-[0_16px_40px_-28px_rgba(15,23,42,0.45)] transition-all hover:-translate-y-0.5 hover:shadow-[0_20px_48px_-28px_rgba(15,23,42,0.5)] dark:border-dark-700 dark:bg-dark-800/95"
            >
              <div class="flex flex-wrap items-start justify-between gap-4">
                <div class="min-w-0">
                  <div class="flex flex-wrap items-center gap-2">
                    <h3 class="break-words text-lg font-semibold leading-7 text-gray-900 dark:text-white">{{ formatJobType(job.job_type) }}</h3>
                    <span class="rounded-full bg-gray-100 px-2.5 py-1 text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                      {{ job.enabled ? t('common.enabled') : t('common.disabled') }}
                    </span>
                    <span class="rounded-full px-2.5 py-1 text-xs font-medium" :class="statusClass(job.last_status)">
                      {{ formatStatus(job.last_status) }}
                    </span>
                  </div>
                </div>

                <div class="flex flex-wrap gap-2">
                  <button type="button" class="btn btn-secondary btn-xs whitespace-nowrap" :disabled="runningJobId === job.id" @click="handleRun(job)">
                    {{ runningJobId === job.id ? t('common.loading') : t('admin.scheduledJobs.runNow') }}
                  </button>
                  <button type="button" class="btn btn-secondary btn-xs whitespace-nowrap" @click="openEdit(job)">
                    {{ t('common.edit') }}
                  </button>
                  <button type="button" class="btn btn-secondary btn-xs whitespace-nowrap" @click="openLogs(job)">
                    {{ t('admin.scheduledJobs.logs') }}
                  </button>
                  <button type="button" class="btn btn-danger btn-xs whitespace-nowrap" @click="handleDelete(job)">
                    {{ t('common.delete') }}
                  </button>
                </div>
              </div>

              <div class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3">
                <div class="rounded-2xl bg-gray-50 px-4 py-3 dark:bg-dark-700/70">
                  <div class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">Cron</div>
                  <div class="mt-1 break-all font-mono text-xs text-gray-700 dark:text-gray-300">{{ job.cron_expression }}</div>
                </div>
                <div class="rounded-2xl bg-gray-50 px-4 py-3 dark:bg-dark-700/70">
                  <div class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    {{ t('admin.scheduledJobs.columns.nextRun') }}
                  </div>
                  <div class="mt-1 text-sm text-gray-700 dark:text-gray-300">{{ formatDate(job.next_run_at) }}</div>
                </div>
                <div class="rounded-2xl bg-gray-50 px-4 py-3 dark:bg-dark-700/70">
                  <div class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    {{ t('admin.scheduledJobs.columns.lastRun') }}
                  </div>
                  <div class="mt-1 text-sm text-gray-700 dark:text-gray-300">{{ formatDate(job.last_run_at) }}</div>
                </div>
              </div>
            </div>
          </div>

          <div v-else class="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.scheduledJobs.empty') }}
          </div>
        </div>
      </div>

      <BaseDialog :show="showEditor" :title="editingId ? t('common.edit') : t('admin.scheduledJobs.create')" width="wide" @close="closeEditor">
        <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.scheduledJobs.columns.type') }}</label>
            <select v-model="form.job_type" class="input w-full" :disabled="Boolean(editingId)">
              <option v-for="option in selectableJobTypeOptions" :key="option.value" :value="option.value">
                {{ option.label }}
              </option>
            </select>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.scheduledJobs.typeAsName') }}</p>
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">Cron</label>
            <input v-model="form.cron_expression" class="input w-full" placeholder="0 * * * *" />
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.scheduledJobs.retentionLimit') }}</label>
            <input v-model.number="form.retention_limit" type="number" min="1" max="1000" class="input w-full" />
          </div>
          <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="form.enabled" type="checkbox" />
            <span>{{ t('common.enabled') }}</span>
          </label>
          <div class="md:col-span-2">
            <template v-if="form.job_type === 'sync_codex_free_group_accounts'">
              <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.scheduledJobs.sourceGroup') }}</label>
              <select v-model.number="syncCodexFreeForm.source_group_id" class="input w-full">
                <option :value="0">{{ t('admin.scheduledJobs.selectSourceGroup') }}</option>
                <option v-for="group in availableGroups" :key="group.id" :value="group.id">
                  {{ group.name }}
                </option>
              </select>
              <label class="mb-1 mt-4 block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.scheduledJobs.targetGroups') }}</label>
              <div class="max-h-64 overflow-y-auto rounded-xl border border-gray-200 p-3 dark:border-dark-600">
                <label
                  v-for="group in targetGroupOptions"
                  :key="group.id"
                  class="flex items-center gap-2 py-1 text-sm text-gray-700 dark:text-gray-300"
                >
                  <input
                    :checked="syncCodexFreeForm.target_group_ids.includes(group.id)"
                    type="checkbox"
                    @change="toggleTargetGroup(group.id)"
                  />
                  <span>{{ group.name }}</span>
                </label>
                <div v-if="!targetGroupOptions.length" class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.scheduledJobs.noTargetGroups') }}
                </div>
              </div>
            </template>
            <template v-else-if="form.job_type === 'update_openai_oauth_model_mapping'">
              <div class="space-y-3">
                <div class="flex items-center justify-between gap-3">
                  <div>
                    <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.scheduledJobs.modelMapping') }}</label>
                    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.scheduledJobs.modelMappingHint') }}</p>
                  </div>
                  <button type="button" class="btn btn-secondary btn-xs" @click="addOpenAIModelMappingRow">
                    {{ t('admin.scheduledJobs.addMapping') }}
                  </button>
                </div>
                <div class="space-y-2">
                  <div
                    v-for="(row, index) in openAIModelMappingRows"
                    :key="row.id"
                    class="grid grid-cols-1 gap-2 rounded-xl border border-gray-200 p-3 dark:border-dark-600 md:grid-cols-[1fr_1fr_auto]"
                  >
                    <div>
                      <label class="mb-1 block text-[11px] font-medium text-gray-500 dark:text-gray-400">{{ t('admin.scheduledJobs.requestModel') }}</label>
                      <Select v-model="row.source" :options="openAIModelCandidateOptions" />
                    </div>
                    <div>
                      <label class="mb-1 block text-[11px] font-medium text-gray-500 dark:text-gray-400">{{ t('admin.scheduledJobs.upstreamModel') }}</label>
                      <Select v-model="row.target" :options="openAIModelCandidateOptions" />
                    </div>
                    <div class="flex items-end">
                      <button type="button" class="btn btn-danger btn-xs w-full md:w-auto" @click="removeOpenAIModelMappingRow(index)">
                        {{ t('common.delete') }}
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            </template>
            <template v-else>
              <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.scheduledJobs.payloadJson') }}</label>
              <textarea v-model="form.payload_json" rows="8" class="input w-full font-mono text-xs"></textarea>
            </template>
          </div>
        </div>
        <template #footer>
          <div class="flex justify-end gap-2">
            <button type="button" class="btn btn-secondary btn-sm" @click="closeEditor">{{ t('common.cancel') }}</button>
            <button type="button" class="btn btn-primary btn-sm" :disabled="saving" @click="submitForm">
              {{ saving ? t('common.loading') : t('common.save') }}
            </button>
          </div>
        </template>
      </BaseDialog>

      <BaseDialog :show="showLogs" :title="t('admin.scheduledJobs.logs')" width="wide" @close="closeLogs">
        <div class="space-y-3">
          <div v-for="run in runs" :key="run.id" class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2">
                <span class="rounded px-2 py-0.5 text-xs" :class="statusClass(run.status)">{{ formatStatus(run.status) }}</span>
                <span class="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400">{{ formatTriggerType(run.trigger_type) }}</span>
              </div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ formatDate(run.created_at) }}</div>
            </div>
            <div v-if="run.message" class="mt-2 text-sm text-gray-700 dark:text-gray-200">{{ formatJobMessage(run.message) }}</div>
            <pre v-if="run.result_json && run.result_json !== '{}'" class="mt-3 overflow-x-auto rounded bg-gray-50 p-3 text-xs text-gray-700 dark:bg-dark-900 dark:text-gray-300">{{ run.result_json }}</pre>
          </div>
          <div v-if="!runs.length" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.scheduledJobs.noLogs') }}</div>
        </div>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAppStore } from '@/stores/app'
import type { AdminGroup, AdminScheduledJob, AdminScheduledJobRun, AdminScheduledOpenAIOAuthModelMappingPayload, AdminScheduledSyncCodexFreeGroupsPayload, CreateAdminScheduledJobRequest, UpdateAdminScheduledJobRequest } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const saving = ref(false)
const runningJobId = ref<number | null>(null)
const jobs = ref<AdminScheduledJob[]>([])
const runs = ref<AdminScheduledJobRun[]>([])
const showEditor = ref(false)
const showLogs = ref(false)
const editingId = ref<number | null>(null)
const currentLogsJobId = ref<number | null>(null)
const availableGroups = ref<AdminGroup[]>([])

const jobTypeOptions = [
  { value: 'backup_postgres', labelKey: 'admin.scheduledJobs.types.backup_postgres' },
  { value: 'data_management_full_backup', labelKey: 'admin.scheduledJobs.types.data_management_full_backup' },
  { value: 'channel_monitor_maintenance', labelKey: 'admin.scheduledJobs.types.channel_monitor_maintenance' },
  { value: 'sync_codex_free_group_accounts', labelKey: 'admin.scheduledJobs.types.sync_codex_free_group_accounts' },
  { value: 'cleanup_error_accounts', labelKey: 'admin.scheduledJobs.types.cleanup_error_accounts' },
  { value: 'update_openai_oauth_model_mapping', labelKey: 'admin.scheduledJobs.types.update_openai_oauth_model_mapping' },
] as const

type OpenAIModelMappingRow = {
  id: number
  source: string
  target: string
}

const openAIModelCandidates = [
  'gpt-5.5',
  'gpt-5.4',
  'gpt-5.4-mini',
  'gpt-5.3-codex',
  'gpt-5.2',
]
const openAIModelCandidateOptions = openAIModelCandidates.map((model) => ({ value: model, label: model }))

const form = reactive<CreateAdminScheduledJobRequest>({
  name: '',
  job_type: 'backup_postgres',
  cron_expression: '0 * * * *',
  enabled: true,
  payload_json: '{}',
  retention_limit: 100,
})

const syncCodexFreeForm = reactive<AdminScheduledSyncCodexFreeGroupsPayload>({
  source_group_id: 0,
  target_group_ids: [],
})
const openAIModelMappingRows = ref<OpenAIModelMappingRow[]>([])
let openAIModelMappingRowID = 1

const targetGroupOptions = computed(() =>
  availableGroups.value.filter((group) => group.id !== syncCodexFreeForm.source_group_id)
)

const selectableJobTypeOptions = computed(() => {
  const usedTypes = new Set(jobs.value.map((job) => job.job_type))
  return jobTypeOptions
    .filter((option) => editingId.value || !usedTypes.has(option.value))
    .map((option) => ({
      value: option.value,
      label: t(option.labelKey),
    }))
})

watch(
  () => syncCodexFreeForm.source_group_id,
  (sourceGroupID) => {
    syncCodexFreeForm.target_group_ids = syncCodexFreeForm.target_group_ids.filter((id) => id !== sourceGroupID)
  }
)

function resetForm() {
  form.job_type = selectableJobTypeOptions.value[0]?.value || 'backup_postgres'
  form.name = formatJobType(form.job_type)
  form.cron_expression = '0 * * * *'
  form.enabled = true
  form.payload_json = '{}'
  form.retention_limit = 100
  syncCodexFreeForm.source_group_id = 0
  syncCodexFreeForm.target_group_ids = []
  openAIModelMappingRows.value = defaultOpenAIModelMappingRows()
}

function openCreate() {
  editingId.value = null
  resetForm()
  showEditor.value = true
}

function openEdit(job: AdminScheduledJob) {
  editingId.value = job.id
  form.job_type = job.job_type
  form.name = formatJobType(job.job_type)
  form.cron_expression = job.cron_expression
  form.enabled = job.enabled
  form.payload_json = job.payload_json || '{}'
  form.retention_limit = job.retention_limit
  if (job.job_type === 'sync_codex_free_group_accounts') {
    try {
      const payload = JSON.parse(job.payload_json || '{}') as Partial<AdminScheduledSyncCodexFreeGroupsPayload>
      syncCodexFreeForm.source_group_id = Number(payload.source_group_id || 0)
      syncCodexFreeForm.target_group_ids = Array.isArray(payload.target_group_ids)
        ? payload.target_group_ids.map((id) => Number(id)).filter((id) => id > 0)
        : []
    } catch {
      syncCodexFreeForm.source_group_id = 0
      syncCodexFreeForm.target_group_ids = []
    }
    openAIModelMappingRows.value = defaultOpenAIModelMappingRows()
  } else if (job.job_type === 'update_openai_oauth_model_mapping') {
    syncCodexFreeForm.source_group_id = 0
    syncCodexFreeForm.target_group_ids = []
    openAIModelMappingRows.value = parseOpenAIModelMappingRows(job.payload_json)
  } else {
    syncCodexFreeForm.source_group_id = 0
    syncCodexFreeForm.target_group_ids = []
    openAIModelMappingRows.value = defaultOpenAIModelMappingRows()
  }
  showEditor.value = true
}

function closeEditor() {
  showEditor.value = false
}

async function openLogs(job: AdminScheduledJob) {
  currentLogsJobId.value = job.id
  showLogs.value = true
  runs.value = await adminAPI.scheduledJobs.listRuns(job.id, 50)
}

function closeLogs() {
  showLogs.value = false
  currentLogsJobId.value = null
  runs.value = []
}

async function loadJobs() {
  loading.value = true
  try {
    jobs.value = await adminAPI.scheduledJobs.list()
  } finally {
    loading.value = false
  }
}

async function loadGroups() {
  const response = await adminAPI.groups.list(1, 500)
  availableGroups.value = response.items ?? []
}

function toggleTargetGroup(groupID: number) {
  const exists = syncCodexFreeForm.target_group_ids.includes(groupID)
  if (exists) {
    syncCodexFreeForm.target_group_ids = syncCodexFreeForm.target_group_ids.filter((id) => id !== groupID)
    return
  }
  syncCodexFreeForm.target_group_ids = [...syncCodexFreeForm.target_group_ids, groupID]
}

function createOpenAIModelMappingRow(source = '', target = ''): OpenAIModelMappingRow {
  return { id: openAIModelMappingRowID++, source, target }
}

function defaultOpenAIModelMappingRows() {
  return [
    createOpenAIModelMappingRow('gpt-5.4', 'gpt-5.4-mini'),
    createOpenAIModelMappingRow('gpt-5.5', 'gpt-5.5'),
    createOpenAIModelMappingRow('gpt-5.4-mini', 'gpt-5.4-mini'),
    createOpenAIModelMappingRow('gpt-5.3-codex', 'gpt-5.4-mini'),
  ]
}

function parseOpenAIModelMappingRows(payloadJSON: string) {
  try {
    const payload = JSON.parse(payloadJSON || '{}') as Partial<AdminScheduledOpenAIOAuthModelMappingPayload>
    const entries = Object.entries(payload.model_mapping || {})
    if (!entries.length) return defaultOpenAIModelMappingRows()
    return entries.map(([source, target]) => createOpenAIModelMappingRow(source, String(target || '')))
  } catch {
    return defaultOpenAIModelMappingRows()
  }
}

function addOpenAIModelMappingRow() {
  openAIModelMappingRows.value = [...openAIModelMappingRows.value, createOpenAIModelMappingRow()]
}

function removeOpenAIModelMappingRow(index: number) {
  openAIModelMappingRows.value = openAIModelMappingRows.value.filter((_, i) => i !== index)
}

function buildOpenAIModelMappingPayload() {
  const mapping: Record<string, string> = {}
  for (const row of openAIModelMappingRows.value) {
    const source = row.source.trim()
    const target = row.target.trim()
    if (!source || !target) continue
    mapping[source] = target
  }
  return { model_mapping: mapping }
}

async function submitForm() {
  saving.value = true
  try {
    form.name = formatJobType(form.job_type)
    if (form.job_type === 'sync_codex_free_group_accounts') {
      if (syncCodexFreeForm.source_group_id <= 0) {
        appStore.showError(t('admin.scheduledJobs.sourceGroupRequired'))
        return
      }
      if (syncCodexFreeForm.target_group_ids.length === 0) {
        appStore.showError(t('admin.scheduledJobs.targetGroupsRequired'))
        return
      }
    } else if (form.job_type === 'update_openai_oauth_model_mapping') {
      if (Object.keys(buildOpenAIModelMappingPayload().model_mapping).length === 0) {
        appStore.showError(t('admin.scheduledJobs.modelMappingRequired'))
        return
      }
    }
    const payloadJSON = form.job_type === 'sync_codex_free_group_accounts'
      ? JSON.stringify({
          source_group_id: syncCodexFreeForm.source_group_id,
          target_group_ids: syncCodexFreeForm.target_group_ids,
        })
      : form.job_type === 'update_openai_oauth_model_mapping'
        ? JSON.stringify(buildOpenAIModelMappingPayload())
        : form.payload_json

    let savedJob: AdminScheduledJob
    if (editingId.value) {
      const payload: UpdateAdminScheduledJobRequest = {
        name: formatJobType(form.job_type),
        cron_expression: form.cron_expression,
        enabled: form.enabled,
        payload_json: payloadJSON,
        retention_limit: form.retention_limit,
      }
      savedJob = await adminAPI.scheduledJobs.update(editingId.value, payload)
      jobs.value = jobs.value.map((job) => job.id === savedJob.id ? savedJob : job)
    } else {
      savedJob = await adminAPI.scheduledJobs.create({
        ...form,
        payload_json: payloadJSON,
      })
      jobs.value = [savedJob, ...jobs.value]
    }
    closeEditor()
    appStore.showSuccess(t('common.saved'))
  } catch (error: any) {
    appStore.showError(error?.message || 'request failed')
  } finally {
    saving.value = false
  }
}

async function handleRun(job: AdminScheduledJob) {
  runningJobId.value = job.id
  try {
    await adminAPI.scheduledJobs.runNow(job.id)
    await loadJobs()
    if (currentLogsJobId.value === job.id) {
      runs.value = await adminAPI.scheduledJobs.listRuns(job.id, 50)
    }
    appStore.showSuccess(t('admin.scheduledJobs.runTriggered'))
  } catch (error: any) {
    appStore.showError(error?.message || 'request failed')
  } finally {
    runningJobId.value = null
  }
}

async function handleDelete(job: AdminScheduledJob) {
  if (!window.confirm(`${t('common.delete')} ${job.name}?`)) return
  try {
    await adminAPI.scheduledJobs.delete(job.id)
    await loadJobs()
    appStore.showSuccess(t('common.deleted'))
  } catch (error: any) {
    appStore.showError(error?.message || 'request failed')
  }
}

function formatDate(value: string | null) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

function formatJobType(value: string) {
  const option = jobTypeOptions.find((item) => item.value === value)
  return option ? t(option.labelKey) : value
}

function formatTriggerType(value: string) {
  const normalized = value.toLowerCase()
  if (normalized === 'manual') return t('admin.scheduledJobs.triggerManual')
  if (normalized === 'scheduled') return t('admin.scheduledJobs.triggerScheduled')
  return value
}

function formatJobMessage(message: string) {
  if (!message) return '-'
  const syncMatch = message.match(/^synced\s+(\d+)\s+accounts\s+from\s+group\s+(\d+)\s+to\s+(\d+)\s+groups?$/i)
  if (syncMatch) {
    return t('admin.scheduledJobs.syncResult', {
      accounts: syncMatch[1],
      sourceGroup: syncMatch[2],
      targetGroups: syncMatch[3],
    })
  }
  const mappingMatch = message.match(/^updated\s+(\d+)\s+openai oauth accounts?$/i)
  if (mappingMatch) {
    return t('admin.scheduledJobs.openAIModelMappingResult', { accounts: mappingMatch[1] })
  }
  return message
}

function formatStatus(value: string) {
  if (!value) return '-'
  const normalized = value.toLowerCase()
  if (normalized === 'success') return t('common.success')
  if (normalized === 'failed') return t('common.error')
  if (normalized === 'running') return t('common.processing')
  return value
}

function statusClass(status: string) {
  if (status === 'success') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  if (status === 'failed') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (status === 'running') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300'
}

onMounted(() => {
  loadJobs()
  loadGroups()
})
</script>
