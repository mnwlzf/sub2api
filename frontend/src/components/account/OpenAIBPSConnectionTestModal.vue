<template>
  <BaseDialog :show="show" :title="t('admin.accounts.bps.testConnection.title')" width="normal" @close="handleClose">
    <div v-if="account" class="space-y-5">
      <div class="flex items-center justify-between rounded-xl border border-cyan-200 bg-cyan-50/60 p-3 dark:border-cyan-800/40 dark:bg-cyan-900/20">
        <div class="min-w-0">
          <div class="truncate font-semibold text-gray-900 dark:text-gray-100">{{ account.name }}</div>
          <div class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.bps.platformName') }} · {{ t('admin.accounts.bps.testConnection.subtitle') }}
          </div>
        </div>
        <PlatformIcon platform="openai_bps" size="md" class="text-cyan-600 dark:text-cyan-300" />
      </div>

      <div>
        <label class="input-label">{{ t('admin.accounts.bps.testConnection.model') }}</label>
        <input
          v-model="model"
          type="text"
          class="input font-mono"
          :placeholder="t('admin.accounts.bps.testConnection.modelPlaceholder')"
          data-testid="bps-test-model"
          @keyup.enter="runTest"
        />
        <p class="input-hint">{{ t('admin.accounts.bps.testConnection.modelHint') }}</p>
      </div>

      <div class="flex items-center justify-between gap-4">
        <div>
          <label class="input-label mb-0">{{ t('admin.accounts.bps.testConnection.compact') }}</label>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.bps.testConnection.compactHint') }}
          </p>
        </div>
        <Toggle v-model="compact" data-testid="bps-test-compact" :aria-label="t('admin.accounts.bps.testConnection.compact')" />
      </div>

      <div v-if="error" class="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800/40 dark:bg-red-900/20 dark:text-red-300" data-testid="bps-test-error">
        {{ error }}
      </div>

      <div v-if="result" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-testid="bps-test-result">
        <div class="flex items-center justify-between">
          <span class="text-sm font-semibold text-gray-900 dark:text-gray-100">
            {{ t('admin.accounts.bps.testConnection.result') }}
          </span>
          <span :class="['inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium', success ? successClass : failureClass]">
            <span :class="['h-1.5 w-1.5 rounded-full', success ? 'bg-emerald-500' : 'bg-red-500']" />
            {{ success ? t('admin.accounts.bps.testConnection.success') : t('admin.accounts.bps.testConnection.failed') }}
          </span>
        </div>
        <dl class="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          <div class="flex items-center justify-between gap-3 sm:block">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.bps.testConnection.httpStatus') }}</dt>
            <dd class="font-mono text-gray-900 dark:text-gray-100" data-testid="bps-test-http-status">
              {{ result.http_status || t('admin.accounts.bps.testConnection.notReached') }}
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3 sm:block">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.bps.testConnection.upstreamModel') }}</dt>
            <dd class="break-all font-mono text-gray-900 dark:text-gray-100" data-testid="bps-test-upstream-model">
              {{ result.upstream_model || '-' }}
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3 sm:block">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.bps.testConnection.model') }}</dt>
            <dd class="break-all font-mono text-gray-900 dark:text-gray-100">{{ result.model || '-' }}</dd>
          </div>
          <div class="flex items-center justify-between gap-3 sm:block">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.bps.testConnection.duration') }}</dt>
            <dd class="font-mono text-gray-900 dark:text-gray-100">{{ result.duration_ms }} ms</dd>
          </div>
          <div class="flex items-center justify-between gap-3 sm:block">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.bps.testConnection.requestId') }}</dt>
            <dd class="break-all font-mono text-gray-900 dark:text-gray-100" data-testid="bps-test-request-id">
              {{ result.request_id || '-' }}
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3 sm:block">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.bps.testConnection.errorCode') }}</dt>
            <dd class="break-all font-mono" :class="result.error_code ? 'text-red-600 dark:text-red-400' : 'text-gray-900 dark:text-gray-100'" data-testid="bps-test-error-code">
              {{ result.error_code || '-' }}
            </dd>
          </div>
        </dl>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.close') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="loading || !account"
          data-testid="bps-test-run"
          @click="runTest"
        >
          <svg v-if="loading" class="-ml-1 mr-2 h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
            <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
            <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
          </svg>
          {{ loading ? t('admin.accounts.bps.testConnection.running') : t('admin.accounts.bps.testConnection.run') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Toggle from '@/components/common/Toggle.vue'
import { adminAPI } from '@/api/admin'
import type { Account, OpenAIBPSTestConnectionResult } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{
  show: boolean
  account: Account | null
}>()

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const model = ref('')
const compact = ref(false)
const loading = ref(false)
const error = ref('')
const result = ref<OpenAIBPSTestConnectionResult | null>(null)

const success = computed(() => {
  const r = result.value
  if (!r) return false
  return r.http_status >= 200 && r.http_status < 300 && !r.error_code
})
const successClass = 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
const failureClass = 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'

function reset(): void {
  model.value = ''
  compact.value = false
  loading.value = false
  error.value = ''
  result.value = null
}

watch(
  () => props.show,
  (visible) => {
    if (visible) reset()
  }
)

async function runTest(): Promise<void> {
  if (!props.account || loading.value) return
  loading.value = true
  error.value = ''
  result.value = null
  try {
    result.value = await adminAPI.accounts.testOpenAIBPSConnection(props.account.id, {
      model: model.value.trim(),
      compact: compact.value
    })
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.accounts.bps.testConnection.requestFailed'))
  } finally {
    loading.value = false
  }
}

function handleClose(): void {
  if (loading.value) return
  emit('close')
}
</script>
