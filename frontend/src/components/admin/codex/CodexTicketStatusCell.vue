<template>
  <div
    v-if="!eligible"
    class="text-sm text-gray-400 dark:text-dark-500"
  >
    -
  </div>
  <div
    v-else
    class="flex min-w-[150px] cursor-pointer flex-col gap-1"
    :title="t('admin.codex.ticket.openCenterHint')"
    @click="emit('open', account)"
  >
    <div v-if="loading && statuses.length === 0" class="flex items-center gap-1.5 text-xs text-gray-400">
      <span class="h-3 w-3 animate-spin rounded-full border-b-2 border-gray-400" />
      {{ t('common.loading') }}
    </div>
    <div v-else-if="error && statuses.length === 0" class="text-xs text-red-500 dark:text-red-400">
      {{ t('admin.codex.ticket.loadFailed') }}
    </div>
    <div v-else-if="statuses.length === 0" class="text-xs text-gray-400 dark:text-dark-500">
      {{ t('admin.codex.ticket.summary.unavailable') }}
    </div>
    <template v-else>
      <div
        v-for="item in visibleItems"
        :key="item.model"
        class="flex min-w-0 items-center gap-1.5"
        :title="itemTooltip(item)"
      >
        <span :class="['h-1.5 w-1.5 flex-shrink-0 rounded-full', dotClass(item.state)]" />
        <span class="max-w-[92px] truncate text-xs text-gray-600 dark:text-gray-300">
          {{ item.model }}
        </span>
        <span :class="['rounded px-1 py-0.5 text-[10px] font-medium', badgeClass(item.state)]">
          {{ t(ticketStateI18nKey(item.state)) }}
          <template v-if="item.state === 'ready' || item.state === 'expiring'">
            · {{ formatTicketRemainingSeconds(item.remainingSeconds) }}
          </template>
        </span>
      </div>
      <span v-if="hiddenCount > 0" class="text-[10px] text-gray-400 dark:text-dark-500">
        {{ t('admin.codex.ticket.moreModels', { count: hiddenCount }) }}
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account, AccountListItem } from '@/types'
import type { CodexTicketStatus } from '@/api/admin/codexTicket'
import {
  classifyTicketStatus,
  formatTicketRemainingSeconds,
  isCodexTicketEligibleAccount,
  normalizeTicketStatuses,
  ticketStateI18nKey,
  type CodexTicketDisplayState
} from '@/features/codexTickets/ticketStatus'
import {
  ensureTicketStatuses,
  getCachedTicketStatuses,
  getTicketStatusesError,
  isTicketStatusesLoading
} from '@/features/codexTickets/useCodexTicketStatuses'

const props = withDefaults(
  defineProps<{
    account: Account | AccountListItem
    /** 父级在票据中心操作后自增，触发重新拉取。 */
    refreshToken?: number
  }>(),
  { refreshToken: 0 }
)

const emit = defineEmits<{
  (e: 'open', account: Account | AccountListItem): void
}>()

const { t } = useI18n()

const eligible = computed(() => isCodexTicketEligibleAccount(props.account))

const response = computed(() => getCachedTicketStatuses(props.account.id))
const statuses = computed<CodexTicketStatus[]>(() => normalizeTicketStatuses(response.value))
const loading = computed(() => isTicketStatusesLoading(props.account.id))
const error = computed(() => getTicketStatusesError(props.account.id))

interface DisplayItem {
  model: string
  state: CodexTicketDisplayState
  remainingSeconds: number
}

const displayItems = computed<DisplayItem[]>(() =>
  statuses.value.map(status => ({
    model: status.model,
    state: classifyTicketStatus(status),
    remainingSeconds: status.remaining_seconds
  }))
)
const visibleItems = computed(() => displayItems.value.slice(0, 2))
const hiddenCount = computed(() => Math.max(0, displayItems.value.length - 2))

const stateColors: Record<CodexTicketDisplayState, { dot: string; badge: string }> = {
  ready: {
    dot: 'bg-emerald-500',
    badge: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  },
  expiring: {
    dot: 'bg-amber-500',
    badge: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  },
  expired: {
    dot: 'bg-orange-500',
    badge: 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-300'
  },
  missing: {
    dot: 'bg-gray-400',
    badge: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  },
  blocked: {
    dot: 'bg-red-500',
    badge: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  },
  paused: {
    dot: 'bg-yellow-500',
    badge: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300'
  },
  disabled: {
    dot: 'bg-slate-400',
    badge: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300'
  },
  not_participating: {
    dot: 'bg-slate-300',
    badge: 'bg-slate-100 text-slate-500 dark:bg-slate-700 dark:text-slate-400'
  },
  unavailable: {
    dot: 'bg-gray-300',
    badge: 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
  }
}

const dotClass = (state: CodexTicketDisplayState) => stateColors[state].dot
const badgeClass = (state: CodexTicketDisplayState) => stateColors[state].badge

const itemTooltip = (item: DisplayItem): string => {
  const status = statuses.value.find(entry => entry.model === item.model)
  if (!status) return item.model
  const parts = [`${item.model}: ${t(ticketStateI18nKey(item.state))}`]
  if (status.expires_at) {
    parts.push(t('admin.codex.ticket.expiresAt', { time: formatDateTime(status.expires_at) }))
  }
  if (status.reason) {
    parts.push(t('admin.codex.ticket.reasonLabel', { reason: status.reason }))
  }
  return parts.join(' · ')
}

const formatDateTime = (value: string): string => {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

const load = async (force = false) => {
  if (!eligible.value) return
  await ensureTicketStatuses(props.account.id, { force })
}

onMounted(() => {
  void load()
})

watch(
  () => props.account.id,
  () => {
    void load()
  }
)

watch(
  () => props.refreshToken,
  (next, previous) => {
    if (next === previous) return
    void load(true)
  }
)
</script>
