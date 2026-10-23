<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.codex.ticketSettings.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.codex.ticketSettings.description') }}
      </p>
    </div>

    <div v-if="loading" class="flex items-center justify-center py-8">
      <div class="h-6 w-6 animate-spin rounded-full border-b-2 border-primary-600"></div>
    </div>

    <div v-else class="space-y-6 p-6">
      <div
        v-if="feedback"
        class="rounded-md px-3 py-2 text-sm"
        :class="
          feedback.kind === 'success'
            ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
            : 'bg-red-50 text-red-600 dark:bg-red-900/20 dark:text-red-300'
        "
      >
        {{ feedback.text }}
      </div>

      <!-- 开关 -->
      <section class="space-y-3">
        <div class="flex items-center justify-between gap-4">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.codex.ticketSettings.enabled.title') }}
            </h3>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.codex.ticketSettings.enabled.description') }}
            </p>
          </div>
          <Toggle v-model="form.enabled" />
        </div>
        <div class="flex items-center justify-between gap-4">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.codex.ticketSettings.allowWithoutTicket.title') }}
            </h3>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.codex.ticketSettings.allowWithoutTicket.description') }}
            </p>
          </div>
          <Toggle v-model="form.allow_without_ticket" />
        </div>
        <div class="flex justify-end">
          <button class="btn btn-primary btn-sm" :disabled="savingSwitches" @click="saveSwitches">
            {{ savingSwitches ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </section>

      <!-- 节奏 -->
      <section class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.codex.ticketSettings.cadence.title') }}
          </h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.ticketSettings.cadence.description') }}
          </p>
        </div>
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <label class="block">
            <span class="input-label">{{ t('admin.codex.ticketSettings.cadence.retryMin') }}</span>
            <input v-model.number="form.cadence.retry_min_seconds" type="number" min="1" class="input" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('admin.codex.ticketSettings.cadence.retryMax') }}</span>
            <input v-model.number="form.cadence.retry_max_seconds" type="number" min="1" class="input" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('admin.codex.ticketSettings.cadence.refresh') }}</span>
            <input v-model.number="form.cadence.refresh_seconds" type="number" min="1" class="input" />
          </label>
        </div>
        <p v-if="cadenceError" class="text-xs text-red-500 dark:text-red-400">
          {{ t(`admin.codex.ticketSettings.cadence.errors.${cadenceError}`) }}
        </p>
        <div class="flex justify-end">
          <button
            class="btn btn-primary btn-sm"
            :disabled="savingCadence || !!cadenceError"
            @click="saveCadence"
          >
            {{ savingCadence ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </section>

      <!-- 代理池 -->
      <section class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.codex.ticketSettings.pool.title') }}
          </h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.ticketSettings.pool.description') }}
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-4">
          <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="poolMode" type="radio" value="all" class="h-4 w-4 text-primary-600" />
            {{ t('admin.codex.ticketSettings.pool.modeAll') }}
          </label>
          <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="poolMode" type="radio" value="custom" class="h-4 w-4 text-primary-600" />
            {{ t('admin.codex.ticketSettings.pool.modeCustom') }}
          </label>
          <span v-if="availableProxyCount != null" class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.ticketSettings.pool.available', { count: availableProxyCount }) }}
          </span>
        </div>
        <div
          v-if="poolMode === 'custom'"
          class="max-h-64 overflow-auto rounded-lg border border-gray-200 dark:border-dark-700"
        >
          <div v-if="proxiesLoading" class="px-3 py-4 text-center text-sm text-gray-500 dark:text-gray-400">
            {{ t('common.loading') }}
          </div>
          <p v-else-if="proxies.length === 0" class="px-3 py-4 text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.ticketSettings.pool.empty') }}
          </p>
          <label
            v-for="proxy in proxies"
            v-else
            :key="proxy.id"
            class="flex items-center justify-between gap-3 border-b border-gray-100 px-3 py-2 last:border-b-0 dark:border-dark-700"
          >
            <span class="flex min-w-0 items-center gap-2">
              <span class="truncate text-sm text-gray-800 dark:text-gray-200">{{ proxy.name }}</span>
              <span class="truncate font-mono text-xs text-gray-500 dark:text-gray-400">
                {{ proxy.host }}:{{ proxy.port }}
              </span>
            </span>
            <span class="flex items-center gap-2">
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ proxy.status }}</span>
              <input
                v-model="selectedProxyIds"
                type="checkbox"
                :value="proxy.id"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              />
            </span>
          </label>
        </div>
        <p v-if="poolError" class="text-xs text-red-500 dark:text-red-400">
          {{ t(`admin.codex.ticketSettings.pool.errors.${poolError}`) }}
        </p>
        <div class="flex justify-end">
          <button class="btn btn-primary btn-sm" :disabled="savingPool || !!poolError" @click="savePool">
            {{ savingPool ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </section>

      <!-- 仅配置文件可调的参数说明 -->
      <div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 text-xs text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400">
        <p class="font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.codex.ticketSettings.configOnly.title') }}
        </p>
        <p class="mt-1">{{ t('admin.codex.ticketSettings.configOnly.description') }}</p>
        <ul class="mt-2 list-inside list-disc space-y-0.5 font-mono">
          <li v-for="key in configOnlyKeys" :key="key">{{ key }}</li>
        </ul>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { adminAPI } from '@/api/admin'
import type { CodexTicketSettings } from '@/api/admin/codexTicket'
import type { Proxy } from '@/types'
import {
  buildTicketProxyPoolPayload,
  normalizeTicketCadence,
  ticketProxyPoolError,
  validateTicketCadence,
  type TicketCadenceError
} from '@/features/codexTickets/settings'

const { t } = useI18n()

interface Feedback {
  kind: 'success' | 'error'
  text: string
}

const loading = ref(true)
const feedback = ref<Feedback | null>(null)
const form = reactive<CodexTicketSettings>({
  enabled: false,
  allow_without_ticket: true,
  cadence: { retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 1800 },
  pool: { mode: 'all', proxy_ids: [] },
  available_proxies: []
})

const proxies = ref<Proxy[]>([])
const proxiesLoading = ref(false)
const poolMode = ref<'all' | 'custom'>('all')
const selectedProxyIds = ref<number[]>([])

const savingSwitches = ref(false)
const savingCadence = ref(false)
const savingPool = ref(false)

const cadenceError = computed<TicketCadenceError | null>(() =>
  validateTicketCadence(normalizeTicketCadence(form.cadence))
)
const poolError = computed(() =>
  ticketProxyPoolError(buildTicketProxyPoolPayload(poolMode.value, selectedProxyIds.value))
)
const availableProxyCount = computed<number | null>(() => {
  const list = form.available_proxies
  return Array.isArray(list) ? list.length : null
})

const configOnlyKeys = [
  'gateway.openai_codex_ticket.target_length',
  'gateway.openai_codex_ticket.ttl_seconds',
  'gateway.openai_codex_ticket.refresh_before_seconds',
  'gateway.openai_codex_ticket.harvest_probe_interval_seconds',
  'gateway.openai_codex_ticket.harvest_attempt_timeout_seconds',
  'gateway.openai_codex_ticket.harvest_retry_min_seconds',
  'gateway.openai_codex_ticket.harvest_retry_max_seconds',
  'gateway.openai_codex_ticket.harvest_refresh_seconds',
  'gateway.openai_codex_ticket.models'
]

const setFeedback = (kind: Feedback['kind'], text: string) => {
  feedback.value = { kind, text }
}

const errorMessage = (error: unknown, fallback: string): string => {
  if (typeof error === 'object' && error !== null && 'message' in error) {
    const message = String((error as { message?: unknown }).message ?? '')
    if (message) return message
  }
  return fallback
}

const applySettings = (settings: CodexTicketSettings) => {
  form.enabled = settings.enabled === true
  form.allow_without_ticket = settings.allow_without_ticket === true
  form.cadence = { ...settings.cadence }
  form.pool = {
    mode: settings.pool?.mode === 'custom' ? 'custom' : 'all',
    proxy_ids: Array.isArray(settings.pool?.proxy_ids) ? [...settings.pool.proxy_ids] : []
  }
  form.available_proxies = settings.available_proxies ?? []
  poolMode.value = form.pool.mode
  selectedProxyIds.value = [...form.pool.proxy_ids]
}

const load = async () => {
  loading.value = true
  feedback.value = null
  try {
    const settings = await adminAPI.codexTicket.getTicketSettings()
    applySettings(settings)
  } catch (error) {
    setFeedback('error', errorMessage(error, t('admin.codex.ticketSettings.loadFailed')))
  } finally {
    loading.value = false
  }
  proxiesLoading.value = true
  try {
    const list = await adminAPI.proxies.getAll()
    proxies.value = Array.isArray(list) ? list : []
  } catch {
    proxies.value = []
  } finally {
    proxiesLoading.value = false
  }
}

const saveSwitches = async () => {
  savingSwitches.value = true
  feedback.value = null
  try {
    const enabled = await adminAPI.codexTicket.setTicketEnabled(form.enabled)
    const allow = await adminAPI.codexTicket.setTicketAllowWithoutTicket(form.allow_without_ticket)
    form.enabled = enabled.enabled
    form.allow_without_ticket = allow.allow_without_ticket
    setFeedback('success', t('common.saved'))
  } catch (error) {
    setFeedback('error', errorMessage(error, t('admin.codex.ticketSettings.saveFailed')))
  } finally {
    savingSwitches.value = false
  }
}

const saveCadence = async () => {
  if (cadenceError.value) return
  savingCadence.value = true
  feedback.value = null
  try {
    const saved = await adminAPI.codexTicket.setTicketCadence(normalizeTicketCadence(form.cadence))
    form.cadence = { ...saved }
    setFeedback('success', t('common.saved'))
  } catch (error) {
    setFeedback('error', errorMessage(error, t('admin.codex.ticketSettings.saveFailed')))
  } finally {
    savingCadence.value = false
  }
}

const savePool = async () => {
  if (poolError.value) return
  savingPool.value = true
  feedback.value = null
  try {
    const saved = await adminAPI.codexTicket.setTicketProxyPool(
      buildTicketProxyPoolPayload(poolMode.value, selectedProxyIds.value)
    )
    form.pool = { mode: saved.mode, proxy_ids: [...saved.proxy_ids] }
    poolMode.value = saved.mode
    selectedProxyIds.value = [...saved.proxy_ids]
    setFeedback('success', t('common.saved'))
  } catch (error) {
    setFeedback('error', errorMessage(error, t('admin.codex.ticketSettings.saveFailed')))
  } finally {
    savingPool.value = false
  }
}

onMounted(() => {
  void load()
})
</script>
