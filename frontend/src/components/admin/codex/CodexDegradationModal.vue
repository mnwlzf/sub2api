<template>
  <BaseDialog
    :show="show && !!account"
    :title="dialogTitle"
    width="wide"
    :close-on-click-outside="false"
    @close="emit('close')"
  >
    <div v-if="account" class="space-y-4">
      <!-- 模型多选 -->
      <div class="rounded-lg border border-gray-200 dark:border-dark-700">
        <div class="flex items-center justify-between border-b border-gray-100 px-4 py-3 dark:border-dark-700">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.codex.degradation.models.title') }}
            </h3>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.codex.degradation.models.description') }}
              <template v-if="bank">
                {{ t('admin.codex.degradation.models.bank', { bank }) }}
              </template>
            </p>
          </div>
          <span class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.degradation.models.selected', { count: selectedModels.length }) }}
          </span>
        </div>
        <div class="grid grid-cols-1 gap-2 px-4 py-3 sm:grid-cols-2 lg:grid-cols-3">
          <div v-if="modelsLoading" class="col-span-full py-2 text-center text-sm text-gray-500 dark:text-gray-400">
            {{ t('common.loading') }}
          </div>
          <p v-else-if="modelOptions.length === 0" class="col-span-full py-2 text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.degradation.models.empty') }}
          </p>
          <label
            v-for="model in modelOptions"
            :key="model"
            class="flex items-center justify-between gap-2 rounded-md border border-gray-100 px-3 py-2 dark:border-dark-700"
          >
            <span class="truncate font-mono text-sm text-gray-700 dark:text-gray-300">{{ model }}</span>
            <input
              v-model="selectedModels"
              type="checkbox"
              :value="model"
              class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </label>
        </div>
      </div>

      <!-- 运行 -->
      <div class="flex flex-wrap items-center gap-3">
        <button class="btn btn-primary" :disabled="running || selectedModels.length === 0" @click="runDiagnose">
          <Icon name="beaker" size="sm" class="mr-1" />
          {{ running ? t('admin.codex.degradation.running') : t('admin.codex.degradation.run') }}
        </button>
        <button class="btn btn-secondary" :disabled="running" @click="loadHistory">
          <Icon name="refresh" size="sm" class="mr-1" />
          {{ t('admin.codex.degradation.history.refresh') }}
        </button>
        <span v-if="running" class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.codex.degradation.runningHint', { count: selectedModels.length, elapsed: elapsedSeconds }) }}
        </span>
        <span v-else-if="selectedModels.length === 0" class="text-xs text-amber-600 dark:text-amber-400">
          {{ t('admin.codex.degradation.models.required') }}
        </span>
      </div>

      <div v-if="runError" class="rounded-md bg-red-50 px-3 py-2 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-300">
        {{ runError }}
      </div>

      <!-- 结果 -->
      <div v-if="result || running" class="space-y-3">
        <div class="flex flex-wrap items-center gap-3">
          <span class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.codex.degradation.result.title') }}
          </span>
          <span
            v-if="result"
            :class="['rounded px-2 py-0.5 text-xs font-medium', degradationStatusBadgeClass(resultStatus)]"
          >
            {{ t(degradationStatusI18nKey(resultStatus)) }}
          </span>
          <span v-if="result?.checked_at" class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.degradation.result.checkedAt', { time: formatDateTime(result.checked_at) }) }}
          </span>
          <span v-if="result?.reason" class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.degradation.result.reason', { reason: result.reason }) }}
          </span>
        </div>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div
            v-for="model in running ? selectedModels : resultModelList"
            :key="model"
            class="rounded-lg border border-gray-200 p-3 dark:border-dark-700"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="truncate font-mono text-sm text-gray-800 dark:text-gray-200">{{ model }}</span>
              <span
                v-if="resultItemFor(model)"
                :class="[
                  'rounded px-2 py-0.5 text-xs font-medium',
                  degradationStatusBadgeClass(resultItemStatus(model))
                ]"
              >
                {{ t(degradationStatusI18nKey(resultItemStatus(model))) }}
              </span>
              <span
                v-else
                class="rounded bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500 dark:bg-dark-700 dark:text-gray-400"
              >
                {{ running ? t('admin.codex.degradation.result.pending') : t('common.noData') }}
              </span>
            </div>
            <dl v-if="resultItemFor(model)" class="mt-2 space-y-1 text-xs">
              <div class="flex justify-between gap-2">
                <dt class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.result.predictedModel') }}
                </dt>
                <dd class="font-mono text-gray-800 dark:text-gray-200">
                  {{ resultItemFor(model)?.predicted_model || '-' }}
                </dd>
              </div>
              <div class="flex justify-between gap-2">
                <dt class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.result.probability') }}
                </dt>
                <dd class="text-gray-800 dark:text-gray-200">
                  {{ formatDegradationProbability(resultItemFor(model)?.probability) }}
                </dd>
              </div>
              <div class="flex justify-between gap-2">
                <dt class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.result.parsedCount') }}
                </dt>
                <dd class="text-gray-800 dark:text-gray-200">
                  {{ resultItemFor(model)?.parsed_count ?? '-' }}
                </dd>
              </div>
              <div class="flex justify-between gap-2">
                <dt class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.result.httpStatus') }}
                </dt>
                <dd class="text-gray-800 dark:text-gray-200">
                  {{ resultItemFor(model)?.http_status || '-' }}
                </dd>
              </div>
              <div v-if="resultItemFor(model)?.reason" class="flex justify-between gap-2">
                <dt class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.result.reasonLabel') }}
                </dt>
                <dd class="text-gray-800 dark:text-gray-200">{{ resultItemFor(model)?.reason }}</dd>
              </div>
            </dl>
          </div>
        </div>
      </div>

      <!-- 历史 -->
      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.codex.degradation.history.title') }}
          </h3>
          <span class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.degradation.history.total', { total: history.length }) }}
          </span>
        </div>
        <div class="max-h-72 overflow-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
            <thead class="sticky top-0 bg-gray-50 dark:bg-dark-800">
              <tr>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.history.time') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.history.model') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.history.status') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.history.predictedModel') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.history.probability') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.degradation.history.source') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-if="historyLoading">
                <td colspan="6" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ t('common.loading') }}
                </td>
              </tr>
              <tr v-else-if="history.length === 0">
                <td colspan="6" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ t('common.noData') }}
                </td>
              </tr>
              <tr v-for="(item, index) in history" v-else :key="`${item.created_at}-${item.model}-${index}`">
                <td class="whitespace-nowrap px-3 py-2 text-gray-700 dark:text-gray-300">
                  {{ formatDateTime(item.created_at) }}
                </td>
                <td class="px-3 py-2 font-mono text-xs text-gray-700 dark:text-gray-300">{{ item.model }}</td>
                <td class="px-3 py-2">
                  <span
                    :class="[
                      'rounded px-1.5 py-0.5 text-xs font-medium',
                      degradationStatusBadgeClass(normalizeDegradationStatus(item.status))
                    ]"
                  >
                    {{ t(degradationStatusI18nKey(normalizeDegradationStatus(item.status))) }}
                  </span>
                </td>
                <td class="px-3 py-2 font-mono text-xs text-gray-700 dark:text-gray-300">
                  {{ item.predicted_model || '-' }}
                </td>
                <td class="px-3 py-2 text-gray-700 dark:text-gray-300">
                  {{ formatDegradationProbability(item.probability) }}
                </td>
                <td class="px-3 py-2 text-xs text-gray-500 dark:text-gray-400">{{ item.source || '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <template #footer>
      <button class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import type { Account } from '@/types'
import type {
  CodexDegradationCheckResult,
  CodexDegradationProbeResult,
  CodexDegradationProbeResultRecord,
  CodexDegradationStatus
} from '@/api/admin/codexDegradation'
import {
  degradationStatusBadgeClass,
  degradationStatusI18nKey,
  formatDegradationProbability,
  normalizeDegradationStatus,
  resolveInitialDiagnosticModels
} from '@/features/codexDegradation/verdict'

const props = defineProps<{
  show: boolean
  account: Account | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const { t } = useI18n()

const dialogTitle = computed(() =>
  props.account
    ? t('admin.codex.degradation.title', { name: props.account.name })
    : t('admin.codex.degradation.title', { name: '-' })
)

const modelOptions = ref<string[]>([])
const bank = ref('')
const modelsLoading = ref(false)
const selectedModels = ref<string[]>([])
const running = ref(false)
const elapsedSeconds = ref(0)
let elapsedTimer: ReturnType<typeof setInterval> | null = null

const result = ref<CodexDegradationCheckResult | null>(null)
const runError = ref<string | null>(null)

const history = ref<CodexDegradationProbeResultRecord[]>([])
const historyLoading = ref(false)

const resultStatus = computed<CodexDegradationStatus>(() =>
  normalizeDegradationStatus(result.value?.status)
)
const resultModelList = computed<string[]>(() => {
  const items = result.value?.items ?? []
  const models = items.map(item => item.model).filter(Boolean)
  return models.length > 0 ? models : (result.value?.models ?? [])
})

const resultItemFor = (model: string): CodexDegradationProbeResult | null =>
  (result.value?.items ?? []).find(item => item.model === model) ?? null
const resultItemStatus = (model: string): CodexDegradationStatus =>
  normalizeDegradationStatus(resultItemFor(model)?.status)

const formatDateTime = (value: string | null | undefined): string => {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

const errorMessage = (error: unknown): string => {
  if (typeof error === 'object' && error !== null && 'message' in error) {
    const message = String((error as { message?: unknown }).message ?? '')
    if (message) return message
  }
  return t('admin.codex.degradation.runFailed')
}

const stopTimer = () => {
  if (elapsedTimer) {
    clearInterval(elapsedTimer)
    elapsedTimer = null
  }
}

const startTimer = () => {
  stopTimer()
  elapsedSeconds.value = 0
  elapsedTimer = setInterval(() => {
    elapsedSeconds.value += 1
  }, 1000)
}

const loadModels = async () => {
  modelsLoading.value = true
  try {
    const [modelsData, settingsData] = await Promise.all([
      adminAPI.codexDegradation.listDegradationModels(),
      adminAPI.codexDegradation.getDegradationSettings().catch(() => null)
    ])
    modelOptions.value = Array.isArray(modelsData.models) ? modelsData.models : []
    bank.value = modelsData.bank || ''
    selectedModels.value = resolveInitialDiagnosticModels(
      settingsData?.models ?? null,
      modelOptions.value
    )
  } catch (error) {
    runError.value = errorMessage(error)
  } finally {
    modelsLoading.value = false
  }
}

const loadHistory = async () => {
  if (!props.account) return
  historyLoading.value = true
  try {
    const data = await adminAPI.codexDegradation.listAccountDegradationResults(props.account.id, 50)
    history.value = Array.isArray(data.items) ? data.items : []
  } catch (error) {
    runError.value = errorMessage(error)
  } finally {
    historyLoading.value = false
  }
}

const runDiagnose = async () => {
  if (!props.account || selectedModels.value.length === 0 || running.value) return
  running.value = true
  runError.value = null
  result.value = null
  startTimer()
  try {
    result.value = await adminAPI.codexDegradation.diagnoseAccount(
      props.account.id,
      selectedModels.value
    )
    await loadHistory()
  } catch (error) {
    runError.value = errorMessage(error)
  } finally {
    running.value = false
    stopTimer()
  }
}

const resetState = () => {
  result.value = null
  runError.value = null
  history.value = []
  selectedModels.value = []
  modelOptions.value = []
  bank.value = ''
  elapsedSeconds.value = 0
  stopTimer()
}

watch(
  () => props.show,
  show => {
    if (!show) {
      stopTimer()
      return
    }
    resetState()
    void loadModels()
    void loadHistory()
  },
  { immediate: true }
)

watch(
  () => props.account?.id,
  () => {
    if (!props.show) return
    resetState()
    void loadModels()
    void loadHistory()
  }
)

onUnmounted(() => {
  stopTimer()
})
</script>
