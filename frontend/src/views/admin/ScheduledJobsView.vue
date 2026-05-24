<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-6">
      <div class="card p-6">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.scheduledJobs.title') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.scheduledJobs.description') }}</p>
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

        <div class="overflow-x-auto">
          <table class="w-full min-w-[980px] text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-left text-xs uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:text-gray-400">
                <th class="py-2 pr-4">{{ t('admin.scheduledJobs.columns.name') }}</th>
                <th class="py-2 pr-4">{{ t('admin.scheduledJobs.columns.type') }}</th>
                <th class="py-2 pr-4">Cron</th>
                <th class="py-2 pr-4">{{ t('admin.scheduledJobs.columns.status') }}</th>
                <th class="py-2 pr-4">{{ t('admin.scheduledJobs.columns.nextRun') }}</th>
                <th class="py-2 pr-4">{{ t('admin.scheduledJobs.columns.lastRun') }}</th>
                <th class="py-2">{{ t('admin.scheduledJobs.columns.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="job in jobs" :key="job.id" class="border-b border-gray-100 align-top dark:border-dark-800">
                <td class="py-3 pr-4">
                  <div class="font-medium text-gray-900 dark:text-white">{{ job.name }}</div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ job.enabled ? t('common.enabled') : t('common.disabled') }}</div>
                </td>
                <td class="py-3 pr-4 font-mono text-xs">{{ job.job_type }}</td>
                <td class="py-3 pr-4 font-mono text-xs">{{ job.cron_expression }}</td>
                <td class="py-3 pr-4">
                  <span class="rounded px-2 py-0.5 text-xs" :class="statusClass(job.last_status)">
                    {{ formatStatus(job.last_status) }}
                  </span>
                  <div v-if="job.last_message" class="mt-1 max-w-xs truncate text-xs text-gray-500 dark:text-gray-400">{{ job.last_message }}</div>
                </td>
                <td class="py-3 pr-4 text-xs">{{ formatDate(job.next_run_at) }}</td>
                <td class="py-3 pr-4 text-xs">{{ formatDate(job.last_run_at) }}</td>
                <td class="py-3">
                  <div class="flex flex-wrap gap-1">
                    <button type="button" class="btn btn-secondary btn-xs" :disabled="runningJobId === job.id" @click="handleRun(job)">
                      {{ runningJobId === job.id ? t('common.loading') : t('admin.scheduledJobs.runNow') }}
                    </button>
                    <button type="button" class="btn btn-secondary btn-xs" @click="openEdit(job)">
                      {{ t('common.edit') }}
                    </button>
                    <button type="button" class="btn btn-secondary btn-xs" @click="openLogs(job)">
                      {{ t('admin.scheduledJobs.logs') }}
                    </button>
                    <button type="button" class="btn btn-danger btn-xs" @click="handleDelete(job)">
                      {{ t('common.delete') }}
                    </button>
                  </div>
                </td>
              </tr>
              <tr v-if="!jobs.length">
                <td colspan="7" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
                  {{ t('admin.scheduledJobs.empty') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <BaseDialog :show="showEditor" :title="editingId ? t('common.edit') : t('admin.scheduledJobs.create')" width="wide" @close="closeEditor">
        <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div class="md:col-span-2">
            <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.scheduledJobs.columns.name') }}</label>
            <input v-model="form.name" class="input w-full" />
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('admin.scheduledJobs.columns.type') }}</label>
            <select v-model="form.job_type" class="input w-full">
              <option value="backup_postgres">{{ t('admin.scheduledJobs.types.backup_postgres') }}</option>
              <option value="data_management_full_backup">{{ t('admin.scheduledJobs.types.data_management_full_backup') }}</option>
              <option value="channel_monitor_maintenance">{{ t('admin.scheduledJobs.types.channel_monitor_maintenance') }}</option>
              <option value="sync_codex_free_group_accounts">同步codex-free分组账号</option>
            </select>
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
              <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">源分组</label>
              <select v-model.number="syncCodexFreeForm.source_group_id" class="input w-full">
                <option :value="0">请选择源分组</option>
                <option v-for="group in availableGroups" :key="group.id" :value="group.id">
                  {{ group.name }}
                </option>
              </select>
              <label class="mb-1 mt-4 block text-xs font-medium text-gray-600 dark:text-gray-400">同步到的分组</label>
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
                  暂无可选目标分组
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
import { computed, onMounted, reactive, ref } from 'vue'
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

function resetForm() {
  form.name = ''
  form.job_type = 'backup_postgres'
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
  form.name = job.name
  form.job_type = job.job_type
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
    if (form.job_type === 'sync_codex_free_group_accounts') {
      if (syncCodexFreeForm.source_group_id <= 0) {
        appStore.showError('请选择源分组')
        return
      }
      if (syncCodexFreeForm.target_group_ids.length === 0) {
        appStore.showError('请至少选择一个目标分组')
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
        name: form.name,
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

function formatStatus(value: string) {
  if (!value) return '-'
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
