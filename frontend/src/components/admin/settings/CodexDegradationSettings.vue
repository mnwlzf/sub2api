<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.codex.degradationSettings.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.codex.degradationSettings.description') }}
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

      <!-- 自动检测设置 -->
      <section class="space-y-3">
        <div class="flex items-center justify-between gap-4">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.codex.degradationSettings.enabled.title') }}
            </h3>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.codex.degradationSettings.enabled.description') }}
            </p>
          </div>
          <Toggle v-model="form.enabled" />
        </div>
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <label class="block">
            <span class="input-label">{{ t('admin.codex.degradationSettings.interval') }}</span>
            <input v-model.number="form.interval_minutes" type="number" min="1" class="input" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('admin.codex.degradationSettings.block') }}</span>
            <input v-model.number="form.block_minutes" type="number" min="1" class="input" />
          </label>
        </div>
        <div>
          <span class="input-label">{{ t('admin.codex.degradationSettings.models.title') }}</span>
          <p class="mb-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.degradationSettings.models.description') }}
            <template v-if="bank">
              {{ t('admin.codex.degradationSettings.models.bank', { bank }) }}
            </template>
          </p>
          <div class="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
            <label
              v-for="model in modelOptions"
              :key="model"
              class="flex items-center justify-between gap-2 rounded-md border border-gray-100 px-3 py-2 dark:border-dark-700"
            >
              <span class="truncate font-mono text-sm text-gray-700 dark:text-gray-300">{{ model }}</span>
              <input
                v-model="form.models"
                type="checkbox"
                :value="model"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              />
            </label>
            <p v-if="modelOptions.length === 0" class="text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.codex.degradationSettings.models.empty') }}
            </p>
          </div>
        </div>
        <p v-if="settingsError" class="text-xs text-red-500 dark:text-red-400">
          {{ t(`admin.codex.degradationSettings.errors.${settingsError}`) }}
        </p>
        <div class="flex flex-wrap justify-end gap-2">
          <button class="btn btn-secondary btn-sm" :disabled="runningNow" @click="runNow">
            <Icon name="play" size="sm" class="mr-1" />
            {{ runningNow ? t('admin.codex.degradationSettings.runNow.running') : t('admin.codex.degradationSettings.runNow.action') }}
          </button>
          <button
            class="btn btn-primary btn-sm"
            :disabled="savingSettings || !!settingsError"
            @click="saveSettings"
          >
            {{ savingSettings ? t('common.saving') : t('common.save') }}
          </button>
        </div>
        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.codex.degradationSettings.runNow.hint') }}
        </p>
      </section>

      <!-- 探测模板 -->
      <section class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.codex.degradationSettings.template.title') }}
          </h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.degradationSettings.template.description') }}
          </p>
        </div>
        <textarea
          v-model="templateText"
          rows="14"
          spellcheck="false"
          class="input w-full font-mono text-xs"
          :placeholder="t('admin.codex.degradationSettings.template.placeholder')"
        ></textarea>
        <p v-if="templateError" class="text-xs text-red-500 dark:text-red-400">{{ templateError }}</p>
        <div class="flex flex-wrap justify-end gap-2">
          <button class="btn btn-secondary btn-sm" :disabled="savingTemplate" @click="restoreDefaultTemplate">
            {{ t('admin.codex.degradationSettings.template.restoreDefault') }}
          </button>
          <button class="btn btn-primary btn-sm" :disabled="savingTemplate" @click="saveTemplate">
            {{ savingTemplate ? t('common.saving') : t('common.save') }}
          </button>
        </div>
        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.codex.degradationSettings.template.hint') }}
        </p>
      </section>

      <!-- 仅配置文件可调的参数说明 -->
      <div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 text-xs text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400">
        <p class="font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.codex.degradationSettings.configOnly.title') }}
        </p>
        <p class="mt-1">{{ t('admin.codex.degradationSettings.configOnly.description') }}</p>
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
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import type { CodexDegradationSettings } from '@/api/admin/codexDegradation'
import {
  normalizeDegradationSettingsPayload,
  normalizeProbeTemplate,
  validateDegradationSettings,
  type DegradationSettingsError
} from '@/features/codexDegradation/settings'

const { t } = useI18n()

interface Feedback {
  kind: 'success' | 'error'
  text: string
}

const loading = ref(true)
const feedback = ref<Feedback | null>(null)
const form = reactive<CodexDegradationSettings>({
  enabled: false,
  interval_minutes: 10,
  block_minutes: 30,
  models: []
})
const modelOptions = ref<string[]>([])
const bank = ref('')
const templateText = ref('')
const defaultTemplate = ref('')

const savingSettings = ref(false)
const savingTemplate = ref(false)
const runningNow = ref(false)
const templateError = ref<string | null>(null)

const settingsError = computed<DegradationSettingsError | null>(() =>
  validateDegradationSettings(
    normalizeDegradationSettingsPayload(form),
    modelOptions.value
  )
)

const configOnlyKeys = [
  'gateway.openai_codex_degradation.probe_timeout_seconds',
  'gateway.openai_codex_degradation.probe_template_file'
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

const applySettings = (settings: CodexDegradationSettings) => {
  form.enabled = settings.enabled === true
  form.interval_minutes = Number(settings.interval_minutes) || 10
  form.block_minutes = Number(settings.block_minutes) || 30
  form.models = Array.isArray(settings.models) ? [...settings.models] : []
}

const load = async () => {
  loading.value = true
  feedback.value = null
  try {
    const [modelsData, settingsData, templateData] = await Promise.all([
      adminAPI.codexDegradation.listDegradationModels(),
      adminAPI.codexDegradation.getDegradationSettings(),
      adminAPI.codexDegradation.getProbeTemplate()
    ])
    modelOptions.value = Array.isArray(modelsData.models) ? modelsData.models : []
    bank.value = modelsData.bank || ''
    applySettings(settingsData)
    templateText.value = templateData.template || templateData.default_template || ''
    defaultTemplate.value = templateData.default_template || ''
  } catch (error) {
    setFeedback('error', errorMessage(error, t('admin.codex.degradationSettings.loadFailed')))
  } finally {
    loading.value = false
  }
}

const saveSettings = async () => {
  if (settingsError.value) return
  savingSettings.value = true
  feedback.value = null
  try {
    const saved = await adminAPI.codexDegradation.saveDegradationSettings(
      normalizeDegradationSettingsPayload(form)
    )
    applySettings(saved)
    setFeedback('success', t('common.saved'))
  } catch (error) {
    setFeedback('error', errorMessage(error, t('admin.codex.degradationSettings.saveFailed')))
  } finally {
    savingSettings.value = false
  }
}

const runNow = async () => {
  runningNow.value = true
  feedback.value = null
  try {
    await adminAPI.codexDegradation.runDegradationNow()
    setFeedback('success', t('admin.codex.degradationSettings.runNow.triggered'))
  } catch (error) {
    setFeedback('error', errorMessage(error, t('admin.codex.degradationSettings.runNow.failed')))
  } finally {
    runningNow.value = false
  }
}

const saveTemplate = async () => {
  savingTemplate.value = true
  templateError.value = null
  feedback.value = null
  try {
    const content = normalizeProbeTemplate(templateText.value)
    if (!content) {
      templateError.value = t('admin.codex.degradationSettings.template.required')
      return
    }
    await adminAPI.codexDegradation.saveProbeTemplate(content)
    setFeedback('success', t('admin.codex.degradationSettings.template.saved'))
  } catch (error) {
    templateError.value = errorMessage(error, t('admin.codex.degradationSettings.template.invalid'))
  } finally {
    savingTemplate.value = false
  }
}

const restoreDefaultTemplate = async () => {
  savingTemplate.value = true
  templateError.value = null
  feedback.value = null
  try {
    // 空串 = 清空数据库覆盖，回退到内嵌默认模板。
    await adminAPI.codexDegradation.saveProbeTemplate('')
    templateText.value = defaultTemplate.value
    setFeedback('success', t('admin.codex.degradationSettings.template.restored'))
  } catch (error) {
    templateError.value = errorMessage(error, t('admin.codex.degradationSettings.template.invalid'))
  } finally {
    savingTemplate.value = false
  }
}

onMounted(() => {
  void load()
})
</script>
