<template>
  <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700 sm:p-5">
    <header class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t('admin.promptTemplates.audit.title') }}
        </h2>
        <p class="mt-1 max-w-3xl text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.audit.description') }}
        </p>
      </div>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="emit('refresh')">
        {{ t('admin.promptTemplates.list.refresh') }}
      </button>
    </header>

    <div
      role="status"
      class="mb-4 rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-xs text-gray-600 dark:border-dark-600 dark:bg-dark-700/50 dark:text-dark-300"
    >
      {{ t('admin.promptTemplates.audit.rawNotice') }}
    </div>

    <p v-if="error" role="alert" class="mb-4 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      {{ error }}
    </p>

    <DataTable
      v-else
      :columns="columns"
      :data="rows"
      :loading="loading"
      row-key="id"
      :virtualize-threshold="50"
    >
      <template #cell-action="{ row }">
        <span class="text-gray-900 dark:text-white">{{ actionLabel(row.event.action) }}</span>
        <span class="ml-1 font-mono text-xs text-gray-400 dark:text-dark-500">{{ row.event.action }}</span>
      </template>

      <template #cell-scope="{ row }">
        <span class="badge badge-gray">{{ scopeLabel(row.event.scope) }}</span>
      </template>

      <template #cell-actor="{ row }">
        <span class="text-gray-700 dark:text-dark-200">{{ actorLabel(row.event) }}</span>
      </template>

      <template #cell-state="{ row }">
        <div class="max-w-xl space-y-1.5">
          <div
            v-for="side in STATE_SIDES"
            :key="side"
            class="flex flex-wrap items-center gap-1.5"
          >
            <span class="w-10 flex-none text-xs text-gray-400 dark:text-dark-500">
              {{ side === 'before' ? t('admin.promptTemplates.audit.stateBefore') : t('admin.promptTemplates.audit.stateAfter') }}
            </span>
            <template v-if="chipsOf(stateOf(row.event, side)).length">
              <span
                v-for="chip in chipsOf(stateOf(row.event, side))"
                :key="`${side}-${chip.key}`"
                class="inline-flex max-w-full items-center gap-1 rounded-md bg-gray-100 px-1.5 py-0.5 text-xs dark:bg-dark-700"
                :title="chip.title"
              >
                <span class="flex-none text-gray-500 dark:text-dark-400">{{ chip.label }}</span>
                <span class="min-w-0 truncate font-mono text-gray-800 dark:text-dark-100">{{ chip.value }}</span>
              </span>
            </template>
            <span v-else class="text-xs text-gray-400 dark:text-dark-500">
              {{ t('admin.promptTemplates.audit.stateNone') }}
            </span>
          </div>

          <button
            type="button"
            class="text-xs text-primary-600 hover:underline dark:text-primary-400"
            :data-test="`audit-raw-${row.id}`"
            @click="toggleRaw(row.id)"
          >
            {{ isRawOpen(row.id) ? t('admin.promptTemplates.audit.hideRaw') : t('admin.promptTemplates.audit.showRaw') }}
          </button>
          <pre
            v-if="isRawOpen(row.id)"
            class="max-h-64 overflow-auto rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-dark-900/60 dark:text-dark-200"
          >{{ rawJson(row.event) }}</pre>
        </div>
      </template>

      <template #empty>
        <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.audit.empty') }}</p>
      </template>
    </DataTable>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import DataTable from '@/components/common/DataTable.vue'
import type { Column } from '@/components/common/types'
import { formatDateTime, shortenSha } from '../format'
import type { PromptAdminEvent } from '../types'

interface AuditRow {
  id: number
  event: PromptAdminEvent
  createdAt: string
}

interface StateChip {
  key: string
  label: string
  value: string
  title?: string
}

const props = defineProps<{
  events: PromptAdminEvent[]
  loading: boolean
  error: string
}>()

const emit = defineEmits<{ refresh: [] }>()

const { t, locale } = useI18n()

/** 后端固定动作枚举；未知值回退到 unknown，绝不臆造。 */
const ACTION_KEYS = new Set([
  'create_template',
  'update_template',
  'archive_template',
  'update_draft',
  'publish_version',
  'set_binding',
  'clear_binding',
  'set_override',
  'clear_override',
])

/** 后端固定作用域枚举。 */
const SCOPE_KEYS = new Set(['template', 'version', 'binding', 'account_override'])

/** before/after 里出现过的字段名 → 可读文案；未登记的键原样展示。 */
const STATE_KEY_KEYS: Record<string, string> = {
  name: 'name',
  archived: 'archived',
  mode: 'mode',
  body_sha256: 'bodySha256',
  body_bytes: 'bodyBytes',
  client_models: 'clientModels',
  supported_profiles: 'supportedProfiles',
  version_no: 'versionNo',
  manifest_sha256: 'manifestSha256',
}

const MODE_VALUES = new Set(['inherit', 'disabled', 'version'])

/** before / after 两侧的固定顺序，模板里循环渲染。 */
const STATE_SIDES = ['before', 'after'] as const

const rawOpenIds = ref<number[]>([])

const columns = computed<Column[]>(() => [
  { key: 'createdAt', label: t('admin.promptTemplates.audit.columns.time'), sortable: true },
  { key: 'action', label: t('admin.promptTemplates.audit.columns.action') },
  { key: 'scope', label: t('admin.promptTemplates.audit.columns.scope') },
  { key: 'actor', label: t('admin.promptTemplates.audit.columns.actor') },
  { key: 'state', label: t('admin.promptTemplates.audit.columns.state') },
])

const rows = computed<AuditRow[]>(() =>
  props.events.map((event) => ({
    id: event.id,
    event,
    createdAt: formatDateTime(event.created_at, locale.value),
  })),
)

function actionLabel(action: string): string {
  return ACTION_KEYS.has(action)
    ? t(`admin.promptTemplates.audit.actions.${action}`)
    : t('admin.promptTemplates.audit.actions.unknown')
}

function scopeLabel(scope: string): string {
  return SCOPE_KEYS.has(scope)
    ? t(`admin.promptTemplates.audit.scopes.${scope}`)
    : t('admin.promptTemplates.audit.scopes.unknown')
}

function actorLabel(event: PromptAdminEvent): string {
  if (event.actor_name) return event.actor_name
  if (event.actor_id) return t('admin.promptTemplates.audit.actorId', { id: event.actor_id })
  return t('admin.promptTemplates.audit.actorUnknown')
}

function stateOf(event: PromptAdminEvent, side: 'before' | 'after'): Record<string, unknown> | undefined {
  return side === 'before' ? event.before_state : event.after_state
}

function stateKeyLabel(key: string): string {
  const mapped = STATE_KEY_KEYS[key]
  return mapped ? t(`admin.promptTemplates.audit.stateKeys.${mapped}`) : key
}

/** 绑定/覆盖的 mode 是固定枚举，翻译成可读文案；未知值原样展示。 */
function modeLabel(value: string): string {
  if (!MODE_VALUES.has(value)) return value
  if (value === 'inherit') return t('admin.promptTemplates.binding.overrides.inherit')
  return t(`admin.promptTemplates.binding.${value}`)
}

function formatStateValue(key: string, value: unknown): { text: string; title?: string } {
  if (value === null || value === undefined) {
    return { text: t('admin.promptTemplates.events.notRecorded') }
  }
  if (typeof value === 'boolean') {
    return {
      text: value
        ? t('admin.promptTemplates.audit.stateTrue')
        : t('admin.promptTemplates.audit.stateFalse'),
    }
  }
  if (Array.isArray(value)) {
    return {
      text: value.length
        ? value.map((item) => String(item)).join(', ')
        : t('admin.promptTemplates.audit.stateEmptyList'),
    }
  }
  if (typeof value === 'object') {
    const text = JSON.stringify(value)
    return { text, title: text }
  }
  const text = String(value)
  if (key.endsWith('_sha256')) {
    return { text: shortenSha(text), title: text }
  }
  if (text.length > 48) {
    return { text: `${text.slice(0, 48)}…`, title: text }
  }
  return { text }
}

function chipsOf(state: Record<string, unknown> | undefined): StateChip[] {
  if (!state) return []
  return Object.entries(state).map(([key, value]) => {
    const label = stateKeyLabel(key)
    if (key === 'mode' && typeof value === 'string') {
      return { key, label, value: modeLabel(value), title: value }
    }
    const formatted = formatStateValue(key, value)
    return { key, label, value: formatted.text, title: formatted.title }
  })
}

function isRawOpen(id: number): boolean {
  return rawOpenIds.value.includes(id)
}

function toggleRaw(id: number) {
  rawOpenIds.value = isRawOpen(id)
    ? rawOpenIds.value.filter((item) => item !== id)
    : [...rawOpenIds.value, id]
}

/** 折叠视图只展示哈希与元数据摘要，展开时才给出原始 JSON。 */
function rawJson(event: PromptAdminEvent): string {
  return JSON.stringify(
    { before_state: event.before_state ?? null, after_state: event.after_state ?? null },
    null,
    2,
  )
}
</script>
