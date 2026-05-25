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
          <div class="hidden lg:block">
            <div class="grid grid-cols-[minmax(0,1.7fr)_160px_minmax(0,1.9fr)_170px_170px_auto] gap-4 px-3 py-2 text-xs font-semibold uppercase tracking-[0.18em] text-gray-500 dark:text-gray-400">
              <div>{{ t('admin.scheduledJobs.columns.name') }}</div>
              <div>Cron</div>
              <div>{{ t('admin.scheduledJobs.columns.status') }}</div>
              <div>{{ t('admin.scheduledJobs.columns.nextRun') }}</div>
              <div>{{ t('admin.scheduledJobs.columns.lastRun') }}</div>
              <div class="text-right">{{ t('admin.scheduledJobs.columns.actions') }}</div>
            </div>

            <div v-if="jobs.length" class="mt-3 space-y-4">
              <div
                v-for="job in jobs"
                :key="job.id"
                class="rounded-3xl border border-gray-200/80 bg-white/95 px-5 py-5 shadow-[0_16px_40px_-28px_rgba(15,23,42,0.45)] transition-all hover:-translate-y-0.5 hover:shadow-[0_20px_48px_-28px_rgba(15,23,42,0.5)] dark:border-dark-700 dark:bg-dark-800/95"
              >
                <div class="grid grid-cols-[minmax(0,1.7fr)_160px_minmax(0,1.9fr)_170px_170px_auto] items-start gap-4">
                  <div class="min-w-0">
                    <div class="flex flex-wrap items-center gap-2">
                      <div class="break-words text-lg font-semibold leading-7 text-gray-900 dark:text-white">{{ formatJobType(job.job_type) }}</div>
                      <span class="rounded-full bg-gray-100 px-2.5 py-1 text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                        {{ job.enabled ? t('common.enabled') : t('common.disabled') }}
                      </span>
                    </div>
                  </div>

                  <div class="rounded-2xl bg-gray-50 px-3 py-2 dark:bg-dark-700/70">
                    <div class="break-all font-mono text-xs leading-5 text-gray-700 dark:text-gray-300">{{ job.cron_expression }}</div>
                  </div>

                  <div class="min-w-0">
                    <span class="rounded-full px-2.5 py-1 text-xs font-medium" :class="statusClass(job.last_status)">
                      {{ formatStatus(job.last_status) }}
                    </span>
                    <div v-if="job.last_message" class="mt-3 break-words text-sm leading-6 text-gray-500 dark:text-gray-400">
                      {{ formatJobMessage(job.last_message) }}
                    </div>
                  </div>

                  <div class="rounded-2xl bg-gray-50 px-3 py-2 text-sm leading-6 text-gray-700 dark:bg-dark-700/70 dark:text-gray-300">
                    {{ formatDate(job.next_run_at) }}
                  </div>
                  <div class="rounded-2xl bg-gray-50 px-3 py-2 text-sm leading-6 text-gray-700 dark:bg-dark-700/70 dark:text-gray-300">
                    {{ formatDate(job.last_run_at) }}
                  </div>

                  <div class="flex min-w-[280px] flex-wrap justify-end gap-2">
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
              </div>
            </div>

            <div v-else class="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.scheduledJobs.empty') }}
            </div>
          </div>

          <div class="space-y-3 lg:hidden">
            <div v-for="job in jobs" :key="job.id" class="rounded-2xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0 flex-1">
                  <div class="break-words text-sm font-semibold text-gray-900 dark:text-white">
                    {{ formatJobType(job.job_type) }}
                  </div>
                </div>
                <span class="shrink-0 rounded-full px-2 py-0.5 text-xs font-medium" :class="statusClass(job.last_status)">
                  {{ formatStatus(job.last_status) }}
                </span>
              </div>

              <div class="mt-2 text-xs text-gray-500 dark:text-gray-400">
                {{ job.enabled ? t('common.enabled') : t('common.disabled') }}
              </div>

              <div class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div class="rounded-2xl bg-gray-50 px-3 py-2 dark:bg-dark-700/70">
                  <div class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">Cron</div>
                  <div class="mt-1 break-all font-mono text-xs text-gray-700 dark:text-gray-300">{{ job.cron_expression }}</div>
                </div>
                <div class="rounded-2xl bg-gray-50 px-3 py-2 dark:bg-dark-700/70">
                  <div class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    {{ t('admin.scheduledJobs.columns.status') }}
                  </div>
                  <div class="mt-1 break-words text-xs text-gray-600 dark:text-gray-300">{{ formatJobMessage(job.last_message) }}</div>
                </div>
                <div class="rounded-2xl bg-gray-50 px-3 py-2 dark:bg-dark-700/70">
                  <div class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    {{ t('admin.scheduledJobs.columns.nextRun') }}
                  </div>
                  <div class="mt-1 text-xs text-gray-700 dark:text-gray-300">{{ formatDate(job.next_run_at) }}</div>
                </div>
                <div class="rounded-2xl bg-gray-50 px-3 py-2 dark:bg-dark-700/70">
                  <div class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    {{ t('admin.scheduledJobs.columns.lastRun') }}
                  </div>
                  <div class="mt-1 text-xs text-gray-700 dark:text-gray-300">{{ formatDate(job.last_run_at) }}</div>
                </div>
              </div>

              <div class="mt-4 grid grid-cols-2 gap-2">
                <button type="button" class="btn btn-secondary btn-sm" :disabled="runningJobId === job.id" @click="handleRun(job)">
                  {{ runningJobId === job.id ? t('common.loading') : t('admin.scheduledJobs.runNow') }}
                </button>
                <button type="button" class="btn btn-secondary btn-sm" @click="openEdit(job)">
                  {{ t('common.edit') }}
                </button>
                <button type="button" class="btn btn-secondary btn-sm" @click="openLogs(job)">
                  {{ t('admin.scheduledJobs.logs') }}
                </button>
                <button type="button" class="btn btn-danger btn-sm" @click="handleDelete(job)">
                  {{ t('common.delete') }}
                </button>
              </div>
            </div>

            <div v-if="!jobs.length" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.scheduledJobs.empty') }}
            </div>
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
                <span class="font-mono text-xs text-gray-500 dark:text-gray-400">{{ run.trigger_type }}</span>
              </div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ formatDate(run.created_at) }}</div>
            </div>
            <div v-if="run.message" class="mt-2 text-sm text-gray-700 dark:text-gray-200">{{ run.message }}</div>
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
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAppStore } from '@/stores/app'
import type { AdminGroup, AdminScheduledJob, AdminScheduledJobRun, AdminScheduledSyncCodexFreeGroupsPayload, CreateAdminScheduledJobRequest, UpdateAdminScheduledJobRequest } from '@/types'

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
] as const

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
  } else {
    syncCodexFreeForm.source_group_id = 0
    syncCodexFreeForm.target_group_ids = []
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
    }
    const payloadJSON = form.job_type === 'sync_codex_free_group_accounts'
      ? JSON.stringify({
          source_group_id: syncCodexFreeForm.source_group_id,
          target_group_ids: syncCodexFreeForm.target_group_ids,
        })
      : form.payload_json

    if (editingId.value) {
      const payload: UpdateAdminScheduledJobRequest = {
        name: formatJobType(form.job_type),
        cron_expression: form.cron_expression,
        enabled: form.enabled,
        payload_json: payloadJSON,
        retention_limit: form.retention_limit,
      }
      await adminAPI.scheduledJobs.update(editingId.value, payload)
    } else {
      await adminAPI.scheduledJobs.create({
        ...form,
        payload_json: payloadJSON,
      })
    }
    closeEditor()
    await loadJobs()
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
